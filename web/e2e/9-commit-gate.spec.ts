import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// The commit dialog checks exactly the bytes it lists, and the server commits only that
// list (ADR-0020). Fake secrets are built at run time so no scanner flags this public file.
const awsKey = `AKIA${"Q7".repeat(8)}`;
const vault = (name: string) => join(env("PM_E2E_VAULT"), name);
const lastSubject = () => execFileSync("git", ["-C", env("PM_E2E_VAULT"), "log", "-1", "--format=%s"], { encoding: "utf8" }).trim();

test("a pasted key is listed in the commit dialog; only Commit anyway commits it", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  writeFileSync(vault("leak.md"), "# Leak\n\n");
  await login(page);
  await page.getByRole("button", { name: "leak.md" }).click();
  await page.evaluate((k) => navigator.clipboard.writeText(`aws_access_key_id = ${k}`), awsKey);
  await page.getByRole("textbox", { name: "Text of leak.md" }).click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.press("ControlOrMeta+V");
  await expect.poll(() => readFileSync(vault("leak.md"), "utf8")).toContain(awsKey);

  await page.getByRole("button", { name: "Commit…" }).click();
  const dialog = page.getByRole("dialog", { name: "Commit changes" });
  const secrets = dialog.getByRole("list", { name: "Possible secrets" });
  await expect(secrets).toContainText("leak.md: AWS access key ID on line 3 (AKIA…)");
  expect(await dialog.textContent()).not.toContain(awsKey);
  await expect(dialog).toContainText("removing them later means rewriting that history");
  await noSeriousA11yViolations(page);

  // No plain Commit, and Commit anyway is never the form's default (Enter) action.
  await expect(dialog.getByRole("button", { name: "Commit", exact: true })).toHaveCount(0);
  const anyway = dialog.getByRole("button", { name: "Commit anyway" });
  await expect(anyway).toHaveAttribute("type", "button");
  const before = lastSubject();
  await dialog.getByLabel("Commit message").fill("Add the leak");
  await dialog.getByLabel("Commit message").press("Enter");
  expect(lastSubject()).toBe(before);

  await anyway.click();
  await expect(dialog.getByRole("status")).toHaveText(/^Committed [0-9a-f]{7}\.$/);
  expect(lastSubject()).toBe("Add the leak");
});

test("a note edited in another tab after the dialog listed it is not committed unseen", async ({ page, context }) => {
  writeFileSync(vault("fresh.md"), "# Fresh\n");
  await login(page);
  await page.getByRole("button", { name: "Commit…" }).click();
  const dialog = page.getByRole("dialog", { name: "Commit changes" });
  await expect(dialog.getByRole("list", { name: "Changes to commit" })).toContainText("fresh.md");
  const commit = dialog.getByRole("button", { name: "Commit", exact: true });
  await expect(commit).toBeEnabled(); // listed and checked

  const tab2 = await context.newPage();
  await tab2.goto("/notes/fresh.md");
  const text = tab2.getByRole("textbox", { name: "Text of fresh.md" });
  await text.click();
  await tab2.keyboard.press("ControlOrMeta+End");
  await tab2.keyboard.type(`from tab two ${awsKey}`);
  await expect.poll(() => readFileSync(vault("fresh.md"), "utf8")).toContain("from tab two");
  await tab2.close();

  const before = lastSubject();
  await dialog.getByLabel("Commit message").fill("Add fresh");
  await commit.click();
  await expect(dialog.getByRole("alert").filter({ hasText: "The changes were updated since you opened this dialog; review them again." })).toBeVisible();
  expect(lastSubject()).toBe(before);
  // The new list is checked again: tab two's key shows, and only Commit anyway is left.
  await expect(dialog.getByRole("list", { name: "Possible secrets" })).toContainText("fresh.md: AWS access key ID on line 2");
  await expect(commit).toHaveCount(0);
});
