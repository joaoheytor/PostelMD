import { execFileSync } from "node:child_process";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// A folder that is a git repository of its own (a clone, a submodule) is left out of
// commits, and the commit dialog says so and what to do (ADR-0026, #118).
const vault = (name: string) => join(env("PM_E2E_VAULT"), name);

// Many repositories, and one name with no place to break: the dialog stays usable.
const longName = `ops-clone-${"x".repeat(180)}`;
const names = ["ops-clone", longName, ...Array.from({ length: 26 }, (_, i) => `ops-clone-${String(i).padStart(2, "0")}`)];

test("the commit dialog names the folders that are git repositories of their own, and doesn't list their files", async ({ page }) => {
  try {
    for (const name of names) {
      mkdirSync(vault(name));
      execFileSync("git", ["init", "--quiet", vault(name)]);
    }
    writeFileSync(join(vault("ops-clone"), "inner.md"), "# Inner\n");
    writeFileSync(vault("beside-clone.md"), "# Beside\n");
    await login(page);
    await page.getByRole("button", { name: "Commit…" }).click();
    const dialog = page.getByRole("dialog", { name: "Commit changes" });
    const changes = dialog.getByRole("list", { name: "Changes to commit" });
    await expect(changes).toContainText("beside-clone.md");
    await expect(changes).not.toContainText("inner.md");

    const leftOut = dialog.getByRole("region", { name: "Left out of commits" });
    await expect(leftOut.getByRole("heading", { name: "Left out of commits" })).toBeVisible();
    const repos = leftOut.getByRole("list", { name: "Git repositories of their own" });
    await expect(repos.getByRole("listitem")).toHaveCount(names.length);
    await expect(repos).toContainText("ops-clone-25");
    await expect(leftOut).toContainText(
      "PostelMD doesn't commit files in these folders. Commit them with their own git, or add them to .gitignore to hide them here.",
    );

    // The list scrolls inside the dialog: the buttons stay in view, and nothing scrolls sideways.
    // Earlier specs leave a key in tabbed.md, so the button may be "Commit anyway".
    await expect(dialog.getByRole("button", { name: /^Commit( anyway)?$/ })).toBeInViewport();
    await expect(dialog.getByRole("button", { name: "Close" })).toBeInViewport();
    for (const box of [dialog, leftOut.getByRole("list", { name: "Left out of commits" })]) {
      expect(await box.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0);
    }
    await noSeriousA11yViolations(page);
  } finally {
    for (const name of names) rmSync(vault(name), { recursive: true, force: true });
    rmSync(vault("beside-clone.md"), { force: true });
  }
});
