import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// Fake secrets, built at run time so no scanner flags this public file.
const awsKey = `AKIA${"Q7".repeat(8)}`;
const githubToken = `ghp_${"aB3".repeat(12)}`;

test("pasting an AWS key warns at once, without showing the key", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  writeFileSync(join(env("PM_E2E_VAULT"), "creds.md"), "# Creds\n\n");
  await login(page);
  await page.getByRole("button", { name: "creds.md" }).click();
  await page.evaluate((k) => navigator.clipboard.writeText(`aws_access_key_id = ${k}`), awsKey);
  await page.getByRole("textbox", { name: "Text of creds.md" }).click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.press("ControlOrMeta+V");

  const warning = page.getByRole("alert").filter({ hasText: "This note looks like it contains a secret" });
  await expect(warning).toContainText("AWS access key ID on line 3 (AKIA…)");
  expect(await warning.textContent()).not.toContain(awsKey);
  await noSeriousA11yViolations(page);

  // A warning, not a block: the note still saves.
  await expect.poll(() => readFileSync(join(env("PM_E2E_VAULT"), "creds.md"), "utf8")).toContain(awsKey);
});

test("a typed token is caught on save; dismissing hides it until the findings change", async ({ page }) => {
  writeFileSync(join(env("PM_E2E_VAULT"), "token.md"), "# Token\n\n");
  await login(page);
  await page.getByRole("button", { name: "token.md" }).click();
  const text = page.getByRole("textbox", { name: "Text of token.md" });
  await text.click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type(`GITHUB_TOKEN=${githubToken}`);

  const warning = page.getByRole("alert").filter({ hasText: "looks like it contains a secret" });
  await expect(warning).toContainText("GitHub token on line 3 (ghp_…)"); // after the autosave
  await warning.getByRole("button", { name: "Dismiss" }).click();
  await expect(warning).toBeHidden();
  await text.click(); // Dismiss had the focus
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type("\nsomething else");
  await expect(page.getByRole("status", { name: "Save status" })).toHaveText("Saved");
  await expect(warning).toBeHidden(); // the same finding stays dismissed

  // Removing it clears the finding, so the same token added back is a new finding: shown again.
  // (Deleted, not typed over: typing over a multi-line selection makes the browser report a
  // blocked style attribute, a separate editor issue.)
  for (let i = 0; i < `GITHUB_TOKEN=${githubToken}\nsomething else`.length; i++) await page.keyboard.press("Backspace");
  await expect.poll(() => readFileSync(join(env("PM_E2E_VAULT"), "token.md"), "utf8")).toBe("# Token\n\n");
  await page.keyboard.type(`rotated ${githubToken}`);
  await expect(warning).toContainText("GitHub token on line 3 (ghp_…)");
});
