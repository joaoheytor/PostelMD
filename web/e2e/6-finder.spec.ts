import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

test("Ctrl+P finds a note by part of its path and opens it", async ({ page }) => {
  const vault = env("PM_E2E_VAULT");
  mkdirSync(join(vault, "ops", "db"), { recursive: true });
  writeFileSync(join(vault, "ops", "db", "failover-redis.md"), "# Redis failover\n");
  writeFileSync(join(vault, "afro.md"), "# Afro\n");
  await login(page);

  // From inside the editor, where the browser would print.
  await page.getByRole("button", { name: "afro.md" }).click();
  await page.getByRole("textbox", { name: "Text of afro.md" }).click();
  await page.keyboard.press("ControlOrMeta+P");
  const finder = page.getByRole("dialog", { name: "Find a note" });
  await expect(finder).toBeVisible();
  const input = finder.getByRole("combobox", { name: "Note path" });
  await expect(input).toBeFocused();
  await input.fill("fr");

  // Word starts rank first: failover-redis before afro.
  const options = finder.getByRole("option");
  await expect(options.first()).toHaveText("ops/db/failover-redis.md");
  await expect(options.first()).toHaveAttribute("aria-selected", "true");
  await expect(finder.getByRole("option", { name: "afro.md" })).toBeVisible();
  await noSeriousA11yViolations(page);

  await input.press("Enter");
  await expect(finder).toBeHidden();
  await expect(page.getByRole("textbox", { name: "Text of ops/db/failover-redis.md" })).toContainText("# Redis failover");
});

test("the finder opens from its button, moves with the arrows, and closes with Escape", async ({ page }) => {
  const vault = env("PM_E2E_VAULT");
  writeFileSync(join(vault, "runbook-a.md"), "# A\n");
  writeFileSync(join(vault, "runbook-b.md"), "# B\n");
  await login(page);

  await page.getByRole("button", { name: "Find a note" }).click();
  const finder = page.getByRole("dialog", { name: "Find a note" });
  const input = finder.getByRole("combobox", { name: "Note path" });
  await input.fill("runbook-");
  await expect(finder.getByRole("option")).toHaveCount(2);
  await input.press("ArrowDown");
  await expect(finder.getByRole("option", { name: "runbook-b.md" })).toHaveAttribute("aria-selected", "true");

  await input.fill("no such note zzz");
  await expect(finder.getByRole("status")).toHaveText("No note matches.");
  await input.press("Escape");
  await expect(finder).toBeHidden();

  await page.getByRole("button", { name: "Find a note" }).click();
  await page.getByRole("combobox", { name: "Note path" }).fill("runbook-b");
  await page.getByRole("option", { name: "runbook-b.md" }).click();
  await expect(page.getByRole("textbox", { name: "Text of runbook-b.md" })).toContainText("# B");
});
