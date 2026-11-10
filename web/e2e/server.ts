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

  const token = await new Promise<string>((resolve, reject) => {
    let out = "";
    const timer = setTimeout(() => reject(new Error(`no setup token within 15 s; output:\n${out}`)), 15_000);
    proc.on("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`postelmd exited (${code}); output:\n${out}`));
    });
    proc.stdout.on("data", (chunk: Buffer) => {
      out += chunk.toString();
      const m = /token\s*\(valid [^)]*\):\s*(\S+)/.exec(out);
      if (m?.[1]) {
        clearTimeout(timer);
        proc.stdout.removeAllListeners("data");
        proc.stdout.resume();
        resolve(m[1]);
      }
    });
  });

  process.env["PM_E2E_TOKEN"] = token;
  process.env["PM_E2E_VAULT"] = join(data, "vaults", "notes");

  return async () => {
    proc.removeAllListeners("exit");
    const exited = new Promise((resolve) => proc.once("exit", resolve));
    proc.kill("SIGTERM");
    await exited;
    rmSync(data, { recursive: true, force: true });
  };
}
