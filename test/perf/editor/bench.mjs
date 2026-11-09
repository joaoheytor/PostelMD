// Runs the editor typing benchmark (PRD measurement procedure) and writes a JSON result.
// Env: SAMPLES (sessions per mode, default 30), RESULT (output path), CHROMIUM (optional
// executable path). Modes alternate session by session so runner drift hits both equally.
import { chromium } from "@playwright/test";
import http from "node:http";
import os from "node:os";
import { readFile, writeFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";

const SAMPLES = Number(process.env.SAMPLES ?? 30);
const KEYS = 150;
const CSP = "default-src 'none'; script-src 'self'; worker-src 'self'; style-src 'self'; img-src 'self'; " +
  "require-trusted-types-for 'script'; trusted-types postelmd-preview dompurify";
const TYPES = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css" };

const server = http.createServer(async (req, res) => {
  const path = new URL(req.url, "http://x").pathname;
  const file = normalize(path === "/" ? "/index.html" : path);
  try {
    const body = await readFile(join("dist", file));
    res.writeHead(200, { "content-type": TYPES[extname(file)] ?? "application/octet-stream", "content-security-policy": CSP });
    res.end(body);
  } catch {
    res.writeHead(404).end();
  }
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const base = `http://127.0.0.1:${server.address().port}/`;

const pct = (xs, p) => {
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.floor(p * s.length))];
};

// One session: fresh context, 4x CPU throttling, 150 keystrokes at line 5,000.
async function session(browser, mode) {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  const violations = [];
  page.on("console", (m) => { if (/Content Security Policy|Trusted Type/i.test(m.text())) violations.push(m.text()); });
  page.on("pageerror", (e) => violations.push(e.message));
  await (await ctx.newCDPSession(page)).send("Emulation.setCPUThrottlingRate", { rate: 4 });
  await page.addInitScript(() => {
    window.__events = [];
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) {
        if (e.name === "keydown") window.__events.push({ d: e.duration, p: e.processingEnd - e.processingStart });
      }
    }).observe({ type: "event", durationThreshold: 16, buffered: true });
  });
  await page.goto(`${base}?mode=${mode}`);
  await page.waitForFunction(() => window.__bench?.ready);
  if (mode === "editor") await page.waitForFunction(() => window.__bench.renders >= 1, null, { timeout: 120_000 });
  await page.evaluate(() => { window.__events.length = 0; window.__bench.goto(); });
  await page.keyboard.type("typing in the middle of a long note, preview open. ".repeat(3).slice(0, KEYS), { delay: 25 });
  if (mode === "editor") await page.waitForFunction(() => window.__bench.renders >= 2, null, { timeout: 120_000 });
  await page.waitForTimeout(300);
  const events = await page.evaluate(() => window.__events);
  const handlers = await page.evaluate(() =>
    [...document.querySelectorAll("#preview *")].filter((el) => [...el.attributes].some((a) => /^on/i.test(a.name))).length);
  if (handlers) violations.push(`${handlers} preview elements kept an on* attribute`);
  await ctx.close();
  if (violations.length) throw new Error(`${mode}: CSP/TT violation or page error: ${violations[0]}`);
  // Event Timing reports only events >= 16 ms; unreported keystrokes were faster than that.
  const durations = [...events.map((e) => e.d), ...Array(Math.max(0, KEYS - events.length)).fill(15)];
  const processing = [...events.map((e) => e.p), ...Array(Math.max(0, KEYS - events.length)).fill(0)];
  return { p50: pct(durations, 0.5), p95: pct(durations, 0.95), procP95: pct(processing, 0.95) };
}

const browser = await chromium.launch(process.env.CHROMIUM ? { executablePath: process.env.CHROMIUM } : {});
const samples = { control: [], editor: [] };
for (let i = 0; i < SAMPLES + 2; i++) {
  for (const mode of i % 2 ? ["editor", "control"] : ["control", "editor"]) {
    const s = await session(browser, mode);
    if (i >= 2) samples[mode].push(s); // the first two rounds are warm-up
  }
}
await browser.close();
server.close();

// PRD procedure: each figure is the median over sessions of the session p95.
const summary = (xs) => ({ paint: pct(xs.map((s) => s.p95), 0.5), processing: pct(xs.map((s) => s.procP95), 0.5) });
const result = {
  runner: { cpus: os.cpus().length, model: os.cpus()[0]?.model ?? "", os: `${os.platform()} ${os.release()}`, browser: browser.version() },
  samples,
  control: summary(samples.control),
  editor: summary(samples.editor),
};
result.ratio = result.editor.paint / result.control.paint;
await writeFile(process.env.RESULT ?? "result.json", JSON.stringify(result, null, 2));
console.log(JSON.stringify({ runner: result.runner, control: result.control, editor: result.editor, ratio: result.ratio.toFixed(3) }));
