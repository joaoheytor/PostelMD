// Summarizes benchmark results from several runners (issue #33): how much each metric varies
// between runners (the noise a regression gate must tolerate) and within one runner.
// Usage: node summarize.mjs <result.json>... ; prints Markdown.
import { readFile } from "node:fs/promises";

const files = process.argv.slice(2);
if (files.length === 0) throw new Error("usage: summarize.mjs <result.json>...");
const runs = await Promise.all(files.map(async (f) => JSON.parse(await readFile(f, "utf8"))));

const mean = (xs) => xs.reduce((a, b) => a + b, 0) / xs.length;
const sd = (xs) => Math.sqrt(mean(xs.map((x) => (x - mean(xs)) ** 2)));
const cv = (xs) => (mean(xs) ? sd(xs) / mean(xs) : 0);
const fmt = (x, d = 1) => x.toFixed(d);

const median = (xs) => { const s = [...xs].sort((a, b) => a - b); return s[Math.floor(s.length / 2)]; };
// PRD procedure: each figure is the median over sessions of the session p95.
const med = (r, mode, key) => median(r.samples[mode].map((s) => s[key]));
const metrics = {
  "control keystroke-to-paint (ms)": runs.map((r) => med(r, "control", "p95")),
  "editor keystroke-to-paint (ms)": runs.map((r) => med(r, "editor", "p95")),
  "editor / control ratio": runs.map((r) => med(r, "editor", "p95") / med(r, "control", "p95")),
  "editor keydown JS processing (ms)": runs.map((r) => med(r, "editor", "procP95")),
};
const within = (mode, key = "p95") => mean(runs.map((r) => cv(r.samples[mode].map((s) => s[key]))));

const lines = [
  `## Editor benchmark noise: ${runs.length} runners`,
  "",
  `Runners: ${[...new Set(runs.map((r) => `${r.runner.cpus} vCPU ${r.runner.model}`))].join("; ")}`,
  "",
  "| Metric | Mean | Std dev | CV | Min | Max |",
  "|---|---|---|---|---|---|",
  ...Object.entries(metrics).map(([name, xs]) =>
    `| ${name} | ${fmt(mean(xs), 3)} | ${fmt(sd(xs), 3)} | ${fmt(100 * cv(xs))}% | ${fmt(Math.min(...xs), 3)} | ${fmt(Math.max(...xs), 3)} |`),
  "",
  `Within one runner, the session-to-session CV is ${fmt(100 * within("control"))}% (control paint), ${fmt(100 * within("editor"))}% (editor paint) and ${fmt(100 * within("editor", "procP95"))}% (editor processing).`,
  "",
  `Budgets (PRD): processing < 4 ms on every runner: ${metrics["editor keydown JS processing (ms)"].every((x) => x < 4) ? "yes" : "NO"}; ratio ≤ 1.10 on every runner: ${metrics["editor / control ratio"].every((x) => x <= 1.1) ? "yes" : "NO"}.`,
];
console.log(lines.join("\n"));
