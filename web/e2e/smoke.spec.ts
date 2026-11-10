import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
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

test("rename the open note into a folder; it stays open at its new path", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "New folder" }).click();
  await page.getByLabel("Folder name in the vault").fill("archive");
  await page.getByRole("button", { name: "Create" }).click();
  await page.getByRole("button", { name: "Vault" }).click();
  await page.getByRole("button", { name: "hello.md" }).click();

  await page.getByRole("button", { name: "Rename or move" }).click();
  await page.getByLabel("New path for hello.md").fill("archive/runbook");
  await page.getByRole("button", { name: "Move", exact: true }).click();

  await expect(page.getByRole("textbox", { name: "Text of archive/runbook.md" })).toContainText("and in the browser");
  await expect(page).toHaveURL(/\/notes\/archive\/runbook\.md$/);
  const vault = env("PM_E2E_VAULT");
  expect(readFileSync(join(vault, "archive", "runbook.md"), "utf8")).toContain("and in the browser");
  expect(existsSync(join(vault, "hello.md"))).toBe(false);
});

test("a move never overwrites an existing note", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "New note" }).click();
  await page.getByLabel("Note name in the vault").fill("draft");
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("textbox", { name: "Text of draft.md" })).toBeVisible();

  await page.getByRole("button", { name: "Rename or move" }).click();
  await page.getByLabel("New path for draft.md").fill("archive/runbook.md");
  await page.getByRole("button", { name: "Move", exact: true }).click();

  await expect(page.getByRole("alert")).toContainText("archive/runbook.md already exists");
  await expect(page.getByRole("textbox", { name: "Text of draft.md" })).toBeVisible();
  expect(readFileSync(join(env("PM_E2E_VAULT"), "archive", "runbook.md"), "utf8")).toContain("and in the browser");
});

test("move a folder; its open note follows it", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "archive" }).click();
  await page.getByRole("button", { name: "runbook.md" }).click();
  await expect(page.getByRole("textbox", { name: "Text of archive/runbook.md" })).toBeVisible();

  await page.getByRole("button", { name: "archive" }).click(); // selects it (and collapses it)
  await page.getByRole("button", { name: "Rename or move folder" }).click();
  await page.getByLabel("New path for archive").fill("ops");
  await page.getByRole("button", { name: "Move", exact: true }).click();

  await expect(page.getByRole("textbox", { name: "Text of ops/runbook.md" })).toBeVisible();
  const vault = env("PM_E2E_VAULT");
  expect(existsSync(join(vault, "ops", "runbook.md"))).toBe(true);
  expect(existsSync(join(vault, "archive"))).toBe(false);
  await noSeriousA11yViolations(page);
});

test("commit the changes with a message", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "Commit…" }).click();
  const dialog = page.getByRole("dialog", { name: "Commit changes" });
  await expect(dialog.getByRole("list", { name: "Changes to commit" })).toContainText("ops/runbook.md");
  await noSeriousA11yViolations(page);

  await dialog.getByLabel("Commit message").fill("Add the first runbooks");
  await dialog.getByRole("button", { name: "Commit", exact: true }).click();
  await expect(dialog.getByRole("status")).toHaveText(/^Committed [0-9a-f]{7}\.$/);
  await expect(dialog).toContainText("No changes since the last commit.");
  const subject = execFileSync("git", ["-C", env("PM_E2E_VAULT"), "log", "-1", "--format=%s"], { encoding: "utf8" });
  expect(subject.trim()).toBe("Add the first runbooks");

  await dialog.getByRole("button", { name: "Close" }).click();
  await expect(dialog).toBeHidden();
});

test("a stale git lock is removed only after the user confirms", async ({ page }) => {
  const lock = join(env("PM_E2E_VAULT"), ".git", "index.lock");
  writeFileSync(lock, "");
  await login(page);
  await page.getByRole("button", { name: "Commit…" }).click();
  const dialog = page.getByRole("dialog", { name: "Commit changes" });
  await expect(dialog.getByRole("heading", { name: "A git lock is blocking commits" })).toBeVisible();

  const remove = dialog.getByRole("button", { name: "Remove the lock" });
  await expect(remove).toBeDisabled();
  await dialog.getByLabel("No other git command is running").check();
  await remove.click();
  await expect(dialog.getByRole("heading", { name: "A git lock is blocking commits" })).toBeHidden();
  expect(existsSync(lock)).toBe(false);
});
