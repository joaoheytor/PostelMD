// The PRD performance budgets for one user (v0.1 acceptance criterion), measured against the
// real binary with the PRD procedure:
//   - typing in a 10,000-line note with the preview open, against a plain <textarea> control,
//     4x CPU throttling, modes alternating, 2 warm-up rounds, the median of session p95s;
//   - autosave: the saves sent while typing (never one per keystroke);
//   - cold start to ready, and idle memory, on a 10,000-note vault.
// Env: BIN (default ../../../bin/postelmd), SAMPLES (sessions per mode, default 30), RESULT
// (output path), CHROMIUM (optional executable path). Exits 1 when a budget is missed.
import { chromium } from "@playwright/test";
import { rmSync } from "node:fs";
import { mkdtemp, writeFile } from "node:fs/promises";
import os from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { bigNote } from "../editor/src/gen.ts";
import { start } from "./server.mjs";
import { writeVault } from "./vault.mjs";

const BIN = process.env.BIN ?? fileURLToPath(new URL("../../../bin/postelmd", import.meta.url));
const SAMPLES = Number(process.env.SAMPLES ?? 30);
const KEYS = 150;
const TEXT = "typing in the middle of a long note, preview open. ".repeat(3).slice(0, KEYS);
const PORT = 18_400 + (process.pid % 500);
const PASSWORD = "perf password, not a secret";
// The preview has rendered the whole note once its last heading is there.
const LAST_HEADING = bigNote().match(/^## (Section \d+)$/gm).at(-1).slice(3);
const budgets = { processingMs: 4, ratio: 1.1, savesPerSession: 3, coldStartMs: 5000, idleRssMB: 80 };

const pct = (xs, p) => {
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.floor(p * s.length))];
};
const median = (xs) => pct(xs, 0.5);

const data = await mkdtemp(join(os.tmpdir(), "postelmd-perf-"));
const vault = join(data, "vaults", "notes");
await writeVault(vault);
let server = start(BIN, data, PORT);
// Never leave a server or a 100 MB data dir behind, whatever happens.
process.on("exit", () => {
  server.proc.kill("SIGKILL");
  rmSync(data, { recursive: true, force: true });
});
await server.ready();

let t = performance.now();
const setup = await post("/api/v1/setup", { token: server.token(), password: PASSWORD, name: "Perf", email: "perf@example.invalid" });
const importMs = performance.now() - t;
if (!setup.ok) throw new Error(`setup: ${setup.status} ${await setup.text()}`);
const cookie = await login();

/** Event Timing for keydowns; only events of 16 ms or more are reported. */
function observe() {
  window.__events = [];
  new PerformanceObserver((list) => {
    for (const e of list.getEntries()) {
      if (e.name === "keydown") window.__events.push({ d: e.duration, p: e.processingEnd - e.processingStart });
    }
  }).observe({ type: "event", durationThreshold: 16, buffered: true });
}

async function session(browser, mode) {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  await ctx.addCookies([{ ...cookie, url: server.base }]);
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => /Content Security Policy|Trusted Type/i.test(m.text()) && errors.push(m.text()));
  let saves = 0;
  page.on("request", (r) => r.method() === "PUT" && r.url().includes("/api/v1/notes/") && saves++);
  await page.addInitScript(observe);
  await (await ctx.newCDPSession(page)).send("Emulation.setCPUThrottlingRate", { rate: 4 });

  if (mode === "control") {
    await page.goto(`${server.base}/healthz`).catch(() => undefined);
    await page.setContent("<textarea aria-label='Control' style='width:600px;height:800px'></textarea>");
    await page.evaluate(observe); // setContent runs no init scripts
    await page.evaluate((text) => {
      const ta = document.querySelector("textarea");
      ta.value = text;
      ta.focus();
      ta.setSelectionRange(text.length / 2, text.length / 2);
    }, bigNote());
  } else {
    await page.addInitScript(() => localStorage.setItem("postelmd.preview", "on"));
    await page.goto(server.base);
    await page.getByRole("button", { name: "big.md" }).click();
    const preview = page.getByLabel("Preview of big.md");
    await preview.getByText(LAST_HEADING, { exact: true }).waitFor({ timeout: 120_000 });
    // Put the cursor in the middle of the note: scroll there, then click a line in view.
    await page.evaluate(() => {
      const scroller = document.querySelector(".pm-editor-host").shadowRoot.querySelector(".cm-scroller");
      scroller.scrollTop = scroller.scrollHeight / 2;
    });
    await page.waitForTimeout(200);
    await page.getByRole("textbox", { name: "Text of big.md" }).click({ position: { x: 200, y: 400 } });
  }

  await page.evaluate(() => (window.__events.length = 0));
  saves = 0;
  await page.keyboard.type(TEXT, { delay: 25 });
  const typingSaves = saves;
  if (mode === "editor") {
    await page.getByLabel("Preview of big.md").getByText("preview open. typing", { exact: false }).first().waitFor({ timeout: 60_000 });
  }
  await page.waitForTimeout(300);
  const events = await page.evaluate(() => window.__events);
  await ctx.close();
  if (errors.length) throw new Error(`${mode}: ${errors[0]}`);
  // Unreported keystrokes were under 16 ms.
  const durations = [...events.map((e) => e.d), ...Array(Math.max(0, KEYS - events.length)).fill(15)];
  const processing = [...events.map((e) => e.p), ...Array(Math.max(0, KEYS - events.length)).fill(0)];
  return { p95: pct(durations, 0.95), procP95: pct(processing, 0.95), saves: typingSaves };
}

const browser = await chromium.launch(process.env.CHROMIUM ? { executablePath: process.env.CHROMIUM } : {});
const samples = { control: [], editor: [] };
for (let i = 0; i < SAMPLES + 2; i++) {
  for (const mode of i % 2 ? ["editor", "control"] : ["control", "editor"]) {
    const s = await session(browser, mode);
    if (i >= 2) samples[mode].push(s);
  }
  process.stderr.write(`round ${i + 1}/${SAMPLES + 2}\n`);
}
await browser.close();

// The note is back to its original text before the restart, so the cold start sees the
// generated vault; the typed edits are saved, which is what a real user leaves behind.
await server.stop();
t = performance.now();
server = start(BIN, data, PORT);
const coldStartMs = await server.ready(t);
await login();
for (const p of ["/api/v1/files", "/api/v1/notes/big.md", "/api/v1/folders/"]) await get(p);
await new Promise((r) => setTimeout(r, 5000));
const idleRssMB = await server.rssMB();
await server.stop();

const result = {
  runner: { cpus: os.cpus().length, model: os.cpus()[0]?.model ?? "", os: `${os.platform()} ${os.release()}`, browser: browser.version() },
  samples,
  editor: { paintMs: median(samples.editor.map((s) => s.p95)), processingMs: median(samples.editor.map((s) => s.procP95)) },
  control: { paintMs: median(samples.control.map((s) => s.p95)) },
  savesPerSession: Math.max(...samples.editor.map((s) => s.saves)),
  importMs,
  coldStartMs,
  idleRssMB,
};
result.ratio = result.editor.paintMs / result.control.paintMs;
const checks = {
  [`keydown processing p95 ${result.editor.processingMs.toFixed(1)} ms < ${budgets.processingMs} ms`]: result.editor.processingMs < budgets.processingMs,
  [`editor/control paint ratio ${result.ratio.toFixed(2)} <= ${budgets.ratio}`]: result.ratio <= budgets.ratio,
  [`saves while typing ${KEYS} keys: at most ${result.savesPerSession} <= ${budgets.savesPerSession}`]: result.savesPerSession <= budgets.savesPerSession,
  [`cold start to ready ${coldStartMs.toFixed(0)} ms < ${budgets.coldStartMs} ms`]: coldStartMs < budgets.coldStartMs,
  [`idle RSS ${idleRssMB.toFixed(1)} MB < ${budgets.idleRssMB} MB`]: idleRssMB < budgets.idleRssMB,
};
result.checks = checks;
await writeFile(process.env.RESULT ?? "result.json", JSON.stringify(result, null, 2));
console.log(`runner: ${result.runner.cpus} CPUs, ${result.runner.model}; Chromium ${result.runner.browser}; ${SAMPLES} sessions per mode`);
console.log(`initial import of 10,000 notes: ${(importMs / 1000).toFixed(1)} s (not budgeted)`);
for (const [what, ok] of Object.entries(checks)) console.log(`${ok ? "PASS" : "FAIL"} ${what}`);
process.exit(Object.values(checks).every(Boolean) ? 0 : 1);

async function post(path, body, headers = {}) {
  return fetch(server.base + path, { method: "POST", headers: { "content-type": "application/json", ...headers }, body: JSON.stringify(body) });
}

async function get(path) {
  const r = await fetch(server.base + path, { headers: { cookie: `${cookie.name}=${cookie.value}` } });
  if (!r.ok) throw new Error(`GET ${path}: ${r.status}`);
  await r.arrayBuffer();
}

async function login() {
  const r = await post("/api/v1/session", { password: PASSWORD });
  const set = r.headers.get("set-cookie") ?? "";
  const m = /^([^=]+)=([^;]+)/.exec(set);
  if (!r.ok || !m) throw new Error(`login: ${r.status}`);
  return { name: m[1], value: m[2] };
}
