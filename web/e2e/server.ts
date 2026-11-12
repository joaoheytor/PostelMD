import { spawn } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { port } from "../playwright.config";

/**
 * Starts bin/postelmd on an empty data dir and reads the one-time setup token from its
 * output (it only ever goes to stdout, ADR-0009). The tests find the token and the vault
 * through the environment, which the worker inherits. Returns the teardown.
 */
export default async function startServer(): Promise<() => Promise<void>> {
  const data = mkdtempSync(join(tmpdir(), "postelmd-e2e-"));
  const proc = spawn(fileURLToPath(new URL("../../bin/postelmd", import.meta.url)), ["serve", "--data", data, "--listen", `127.0.0.1:${port}`], {
    stdio: ["ignore", "pipe", "inherit"],
  });

  // Stops the server (if it is running) and removes its data. Every exit path calls it:
  // startup failure, timeout and the global teardown. It runs once, however often called.
  let stopped: Promise<void> | undefined;
  const teardown = (): Promise<void> =>
    (stopped ??= (async () => {
      proc.removeAllListeners("exit");
      // pid is undefined when the spawn itself failed: there is no process to wait for.
      if (proc.pid !== undefined && proc.exitCode === null && proc.signalCode === null) {
        const exited = new Promise((resolve) => proc.once("exit", resolve));
        proc.kill("SIGTERM");
        await exited;
      }
      rmSync(data, { recursive: true, force: true });
    })());

  const token = await new Promise<string>((resolve, reject) => {
    let out = "";
    const fail = (msg: string): void => {
      clearTimeout(timer);
      proc.stdout.removeAllListeners("data");
      teardown().then(
        () => reject(new Error(`${msg}; output:\n${out}`)),
        (err: unknown) => reject(new Error(`${msg}; cleanup failed (${String(err)}); output:\n${out}`)),
      );
    };
    const timer = setTimeout(() => fail("no setup token within 15 s"), 15_000);
    proc.on("error", (err) => fail(`postelmd did not start (${err.message})`));
    proc.on("exit", (code) => fail(`postelmd exited (${code})`));
    proc.stdout.on("data", (chunk: Buffer) => {
      out += chunk.toString();
      const m = /token\s*\(valid [^)]*\):\s*(\S+)/.exec(out);
      if (m?.[1]) {
        clearTimeout(timer);
        proc.stdout.removeAllListeners("data");
        proc.stdout.resume();
        // From here on, a crash surfaces as failing tests; the data stays until teardown.
        proc.removeAllListeners("exit");
        resolve(m[1]);
      }
    });
  });

  process.env["PM_E2E_TOKEN"] = token;
  process.env["PM_E2E_VAULT"] = join(data, "vaults", "notes");

  return teardown;
}
