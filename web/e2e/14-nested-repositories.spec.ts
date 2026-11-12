import { execFileSync } from "node:child_process";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// A folder that is a git repository of its own (a clone, a submodule) is left out of
// commits, and the commit dialog says so (ADR-0026, #118).
const vault = (name: string) => join(env("PM_E2E_VAULT"), name);

test("the commit dialog names a folder that is a git repository of its own, and doesn't list its files", async ({ page }) => {
  const clone = vault("ops-clone");
  try {
    mkdirSync(clone);
    execFileSync("git", ["init", "--quiet", clone]);
    writeFileSync(join(clone, "inner.md"), "# Inner\n");
    writeFileSync(vault("beside-clone.md"), "# Beside\n");
    await login(page);
    await page.getByRole("button", { name: "Commit…" }).click();
    const dialog = page.getByRole("dialog", { name: "Commit changes" });
    await expect(dialog.getByRole("list", { name: "Changes to commit" })).toContainText("beside-clone.md");
    await expect(dialog.getByRole("list", { name: "Changes to commit" })).not.toContainText("inner.md");
    await expect(dialog.getByRole("list", { name: "Left out of commits" }).getByRole("listitem")).toHaveText([
      "Folders that are git repositories of their own, left out: ops-clone",
    ]);
    await noSeriousA11yViolations(page);
  } finally {
    rmSync(clone, { recursive: true, force: true });
    rmSync(vault("beside-clone.md"), { force: true });
  }
});
