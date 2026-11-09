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

const metrics = {
  "control p95 (ms)": runs.map((r) => r.control.p95),
  "editor p95 (ms)": runs.map((r) => r.editor.p95),
  "editor / control ratio": runs.map((r) => r.ratio),
  "editor keydown JS p95 (ms)": runs.map((r) => r.editor.procP95),
};
const within = (mode) => mean(runs.map((r) => cv(r.samples[mode].map((s) => s.p95))));

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
  `Within one runner, the session-to-session CV of p95 is ${fmt(100 * within("control"))}% (control) and ${fmt(100 * within("editor"))}% (editor).`,
  "",
  `Gate tolerance suggested by this run: max(10%, 3 × CV of the ratio) = ${fmt(Math.max(10, 300 * cv(metrics["editor / control ratio"])))}%.`,
];
console.log(lines.join("\n"));
