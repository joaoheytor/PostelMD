// Starts bin/postelmd on a data dir and stops it; the setup token only ever goes to stdout
// (ADR-0009), so it is read from there.
import { spawn } from "node:child_process";
import { readFile } from "node:fs/promises";

export function start(bin, data, port) {
  const proc = spawn(bin, ["serve", "--data", data, "--listen", `127.0.0.1:${port}`], { stdio: ["ignore", "pipe", "inherit"] });
  let out = "";
  proc.stdout.on("data", (c) => (out += c.toString()));
  const base = `http://127.0.0.1:${port}`;
  return {
    proc,
    base,
    /** Resolves once /readyz answers, with the milliseconds since the process started. */
    async ready(t0 = performance.now()) {
      for (;;) {
        if (proc.exitCode !== null) throw new Error(`postelmd exited (${proc.exitCode}):\n${out}`);
        try {
          if ((await fetch(`${base}/readyz`)).ok) return performance.now() - t0;
        } catch { /* not listening yet */ }
        await new Promise((r) => setTimeout(r, 10));
      }
    },
    token: () => /token\s*\(valid [^)]*\):\s*(\S+)/.exec(out)?.[1],
    async rssMB() {
      const status = await readFile(`/proc/${proc.pid}/status`, "utf8");
      return Number(/VmRSS:\s+(\d+) kB/.exec(status)[1]) / 1024;
    },
    async stop() {
      const exited = new Promise((r) => proc.once("exit", r));
      proc.kill("SIGTERM");
      await exited;
    },
  };
}
