import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// One user and one vault: each test builds on the one before.
test.describe.configure({ mode: "serial" });

const password = "correct horse battery staple";

function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is unset: run through playwright.config.ts`);
  return v;
}

async function noSeriousA11yViolations(page: Page): Promise<void> {
  const { violations } = await new AxeBuilder({ page }).analyze();
  expect(violations.filter((v) => v.impact === "serious" || v.impact === "critical")).toEqual([]);
}

async function login(page: Page): Promise<void> {
  await page.goto("/");
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByRole("navigation", { name: "Notes" })).toBeVisible();
}

test("first-run setup with the server's token", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Set up PostelMD" })).toBeVisible();
  await noSeriousA11yViolations(page);

  await page.getByLabel("Setup token").fill(env("PM_E2E_TOKEN"));
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Repeat the password").fill(password);
  await page.getByLabel("Name").fill("E2E Tester");
  await page.getByLabel("Email").fill("e2e@example.invalid");
  await page.getByRole("button", { name: "Finish setup" }).click();

  await expect(page.getByRole("navigation", { name: "Notes" })).toBeVisible();
});

test("a wrong password is refused", async ({ page }) => {
  await page.goto("/");
  await noSeriousA11yViolations(page);
  await page.getByLabel("Password").fill("not the password");
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByRole("alert")).toHaveText("Wrong password.");
});

test("create a note, edit it, and the edit is on disk after a reload", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "New note" }).click();
  await page.getByLabel("Note name in the vault").fill("hello");
  await page.getByRole("button", { name: "Create" }).click();

  const text = page.getByRole("textbox", { name: "Text of hello.md" });
  await expect(text).toBeVisible();
  await text.press("ControlOrMeta+End");
  await text.pressSequentially("Saved by the browser.");
  await expect(page.getByRole("status")).toHaveText("Saved");
  expect(readFileSync(join(env("PM_E2E_VAULT"), "hello.md"), "utf8")).toContain("Saved by the browser.");

  await page.reload();
  await expect(page.getByRole("textbox", { name: "Text of hello.md" })).toContainText("Saved by the browser.");
  await noSeriousA11yViolations(page);
});

test("an edit made outside the editor is a conflict, not an overwrite", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "hello.md" }).click();
  const text = page.getByRole("textbox", { name: "Text of hello.md" });
  await expect(text).toBeVisible();

  const file = join(env("PM_E2E_VAULT"), "hello.md");
  writeFileSync(file, "# Changed on disk\n");
  await text.press("ControlOrMeta+End");
  await text.pressSequentially(" and in the browser");

  await expect(page.getByRole("heading", { name: "This note changed outside this editor" })).toBeVisible();
  expect(readFileSync(file, "utf8")).toBe("# Changed on disk\n");

  await page.getByRole("button", { name: "Keep my version" }).click();
  await expect(page.getByRole("status")).toHaveText("Saved");
  expect(readFileSync(file, "utf8")).toContain("and in the browser");
});
