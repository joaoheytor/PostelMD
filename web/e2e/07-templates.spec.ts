import { readFileSync } from "node:fs";
import { join } from "node:path";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

test("a new note can start from a template, with its placeholders filled in", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "New note" }).click();
  await page.getByLabel("Note name in the vault").fill("Disk full on db-1");
  await page.getByLabel("Template").selectOption("Postmortem");

  const fields = page.getByRole("group", { name: "Fill in (optional)" });
  await expect(fields.getByRole("textbox")).toHaveCount(2);
  await fields.getByLabel("Severity").fill("SEV2");
  await noSeriousA11yViolations(page);
  await page.getByRole("button", { name: "Create" }).click();

  await expect(page.getByRole("textbox", { name: "Text of Disk full on db-1.md" })).toContainText("# Disk full on db-1");
  const note = readFileSync(join(env("PM_E2E_VAULT"), "Disk full on db-1.md"), "utf8");
  expect(note).toMatch(/^# Disk full on db-1\n/);
  expect(note).toMatch(/\| \d{4}-\d{2}-\d{2} \| SEV2 \| Draft \| \{\{author\}\} \|/);
  expect(note).toContain("## Action items");
});

test("switching back to a blank note drops the template's fields", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "New note" }).click();
  await page.getByLabel("Template").selectOption("Runbook");
  await expect(page.getByRole("group", { name: "Fill in (optional)" })).toBeVisible();
  await page.getByLabel("Template").selectOption("Blank note");
  await expect(page.getByRole("group", { name: "Fill in (optional)" })).toBeHidden();
  await page.getByLabel("Note name in the vault").fill("plain");
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("textbox", { name: "Text of plain.md" })).toBeVisible();
  expect(readFileSync(join(env("PM_E2E_VAULT"), "plain.md"), "utf8")).toBe("# plain\n\n");
});
