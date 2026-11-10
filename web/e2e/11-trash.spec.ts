import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Page } from "@playwright/test";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// The trash (ADR-0024): list what was deleted and restore it, never over another file.

const vault = (...p: string[]) => join(env("PM_E2E_VAULT"), ...p);
const onDisk = (p: string) => () => (existsSync(vault(p)) ? readFileSync(vault(p), "utf8") : "<missing>");

async function deleteNote(page: Page, name: string): Promise<void> {
  await page.getByRole("button", { name, exact: true }).click();
  page.once("dialog", (d) => void d.accept());
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
}

test("a deleted note comes back from the trash with its content", async ({ page }) => {
  rmSync(vault(".postelmd-trash"), { recursive: true, force: true }); // emptied by hand
  const text = "# Trash me\n\nKeep this text.\n";
  writeFileSync(vault("trash-me.md"), text);
  await login(page);

  await page.getByRole("button", { name: "Trash", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Trash" });
  await expect(dialog.getByText("The trash is empty.")).toBeVisible();
  await dialog.getByRole("button", { name: "Close" }).click();

  await deleteNote(page, "trash-me.md");
  expect(existsSync(vault("trash-me.md"))).toBe(false);

  await page.getByRole("button", { name: "Trash", exact: true }).click();
  const item = dialog.getByRole("list", { name: "Deleted items" }).getByRole("listitem").filter({ hasText: "trash-me.md" });
  await expect(item).toContainText("deleted");
  await noSeriousA11yViolations(page);
  const restore = item.getByRole("button", { name: "Restore trash-me.md" });
  const to = dialog.getByLabel("Restore trash-me.md to");
  // Escape and Cancel close the form, not the dialog, and give the focus back to the button.
  await restore.click();
  await expect(to).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(restore).toBeFocused();
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(to).toBeFocused();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(restore).toBeFocused();

  await restore.click();
  await expect(to).toBeFocused();
  await expect(to).toHaveValue("trash-me.md");
  await noSeriousA11yViolations(page);
  await page.keyboard.press("Enter");

  await expect(dialog).toHaveCount(0);
  await expect.poll(onDisk("trash-me.md")).toBe(text);
  await expect(page.getByRole("button", { name: "trash-me.md", exact: true })).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("textbox", { name: "Text of trash-me.md" })).toContainText("Keep this text.");
});

test("restoring onto a name in use says so, and another name restores it", async ({ page }) => {
  writeFileSync(vault("clash.md"), "# First\n");
  await login(page);
  await deleteNote(page, "clash.md");
  writeFileSync(vault("clash.md"), "# Second\n"); // a new note took the name meanwhile

  await page.getByRole("button", { name: "Trash", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Trash" });
  await dialog.getByRole("button", { name: "Restore clash.md" }).click();
  await page.keyboard.press("Enter");
  await expect(dialog.getByRole("alert")).toHaveText(
    "Couldn't restore clash.md: the vault already has something named clash.md. Type another name.",
  );
  const to = dialog.getByLabel("Restore clash.md to");
  await expect(to).toBeFocused();
  expect(readFileSync(vault("clash.md"), "utf8")).toBe("# Second\n");

  await to.fill("clash-first");
  await page.keyboard.press("Enter");
  await expect(dialog).toHaveCount(0);
  await expect.poll(onDisk("clash-first.md")).toBe("# First\n");
  expect(readFileSync(vault("clash.md"), "utf8")).toBe("# Second\n");
  await expect(page.getByRole("button", { name: "clash-first.md", exact: true })).toBeVisible();
});
