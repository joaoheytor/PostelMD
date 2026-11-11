import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Dialog, Page } from "@playwright/test";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// The trash (ADR-0024): list what was deleted and restore it, never over another file.

const vault = (...p: string[]) => join(env("PM_E2E_VAULT"), ...p);
const onDisk = (p: string) => () => (existsSync(vault(p)) ? readFileSync(vault(p), "utf8") : "<missing>");

/** A folder's entry in the tree: its name follows the ▸ or ▾ marker. */
const treeFolder = (page: Page, name: string) =>
  page.getByRole("navigation", { name: "Notes" }).getByRole("list").getByRole("button", { name: new RegExp(`^\\S ${name}$`) });

/** The tree's status region: empty until a delete completes, cleared by the next action. */
const treeStatus = (page: Page) => page.getByRole("status", { name: "File tree status" });

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

test("Delete folder moves it to the trash, open note and all, and it restores whole", async ({ page }) => {
  mkdirSync(vault("old-runbooks", "db"), { recursive: true });
  writeFileSync(vault("old-runbooks", "db", "failover.md"), "# Failover\n");
  await login(page);
  const folder = treeFolder(page, "old-runbooks");
  await folder.click();
  await treeFolder(page, "db").click();
  await page.getByRole("button", { name: "failover.md", exact: true }).click();
  const text = page.getByRole("textbox", { name: "Text of old-runbooks/db/failover.md" });
  await text.press("ControlOrMeta+End");
  await page.keyboard.type("Unsaved line.");

  // The selected folder is the open note's: select its parent to delete the whole tree.
  await folder.click();
  let asked = "";
  page.once("dialog", (d) => {
    asked = d.message();
    void d.accept();
  });
  await page.getByRole("button", { name: "Delete folder old-runbooks" }).click();
  await expect(folder).toHaveCount(0);
  expect(asked).toBe("Move the folder old-runbooks and everything in it to the trash? You can restore it from the Trash.");
  await expect(treeStatus(page)).toHaveText("Moved the folder old-runbooks to the trash.");
  await expect(page.getByRole("button", { name: "Vault" })).toBeFocused();
  await expect(text).toHaveCount(0);
  expect(existsSync(vault("old-runbooks"))).toBe(false);

  await page.getByRole("button", { name: "Trash", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Trash" });
  await expect(dialog.getByRole("listitem").filter({ hasText: "old-runbooks (folder)" })).toBeVisible();
  await dialog.getByRole("button", { name: "Restore old-runbooks" }).click();
  await page.keyboard.press("Enter");
  await expect(dialog).toHaveCount(0);
  // The edit typed before the delete was saved before it went, so it came back too.
  await expect.poll(onDisk("old-runbooks/db/failover.md")).toBe("# Failover\nUnsaved line.");
  await expect(folder).toBeVisible();
});

test("Delete folder asks again when the open note inside it can't be saved", async ({ page }) => {
  mkdirSync(vault("clashing"), { recursive: true });
  writeFileSync(vault("clashing", "note.md"), "# Note\n");
  await login(page);
  const folder = treeFolder(page, "clashing");
  await folder.click();
  await page.getByRole("button", { name: "note.md", exact: true }).click();
  writeFileSync(vault("clashing", "note.md"), "# Changed on disk\n");
  await page.getByRole("textbox", { name: "Text of clashing/note.md" }).pressSequentially("x");
  await expect(page.getByRole("heading", { name: "This note changed in another tab or outside this editor" })).toBeVisible();

  await folder.click();
  const asked: string[] = [];
  const answer = (d: Dialog) => {
    asked.push(d.message());
    void (asked.length === 1 ? d.accept() : d.dismiss());
  };
  page.on("dialog", answer);
  await page.getByRole("button", { name: "Delete folder clashing" }).click();
  await expect.poll(() => asked.length).toBe(2);
  page.off("dialog", answer);
  expect(asked[1]).toBe("Some edits to clashing/note.md aren't saved. Delete the folder anyway?");
  expect(existsSync(vault("clashing", "note.md"))).toBe(true);
  await expect(page.getByRole("textbox", { name: "Text of clashing/note.md" })).toBeVisible();
});

test("Delete folder by keyboard: Cancel keeps it, OK deletes it, and a failure keeps the focus", async ({ page }) => {
  mkdirSync(vault("by-keys"), { recursive: true });
  writeFileSync(vault("by-keys", "a.md"), "# A\n");
  mkdirSync(vault("gone-meanwhile"), { recursive: true });
  await login(page);
  const answers: ("accept" | "dismiss")[] = ["dismiss", "accept", "accept"];
  const asked: string[] = [];
  const answer = (d: Dialog) => {
    asked.push(d.message());
    void (answers.shift() === "accept" ? d.accept() : d.dismiss());
  };
  page.on("dialog", answer);

  await treeFolder(page, "by-keys").focus();
  await page.keyboard.press("Enter"); // selects it
  const del = page.getByRole("button", { name: "Delete folder by-keys" });
  await del.focus();
  // Cancel: nothing moves, the focus stays on the button, and nothing is announced.
  await page.keyboard.press("Enter");
  await expect.poll(() => asked.length).toBe(1);
  await expect(del).toBeFocused();
  await expect(treeFolder(page, "by-keys")).toBeVisible();
  await expect(treeStatus(page)).toHaveText("");
  expect(existsSync(vault("by-keys", "a.md"))).toBe(true);

  // OK: it goes, the status says so, and the focus moves to its parent.
  await page.keyboard.press("Enter");
  await expect(treeStatus(page)).toHaveText("Moved the folder by-keys to the trash.");
  await expect(page.getByRole("button", { name: "Vault" })).toBeFocused();
  expect(existsSync(vault("by-keys"))).toBe(false);

  // The next action in the tree clears it.
  await treeFolder(page, "gone-meanwhile").focus();
  await page.keyboard.press("Enter");
  await expect(treeStatus(page)).toHaveText("");

  // A delete that fails says why and leaves the focus where it was.
  rmSync(vault("gone-meanwhile"), { recursive: true });
  const gone = page.getByRole("button", { name: "Delete folder gone-meanwhile" });
  await gone.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toContainText("Couldn't delete gone-meanwhile");
  await expect(gone).toBeFocused();
  await expect(treeStatus(page)).toHaveText("");
  page.off("dialog", answer);
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

  // A note's name can't start with a dot, as for New note and Rename.
  await to.fill(".hidden");
  await page.keyboard.press("Enter");
  await expect(dialog.getByRole("alert")).toHaveText(/^Couldn't restore clash\.md: A note name can't start with a dot/);
  await expect(to).toBeFocused();
  expect(existsSync(vault(".hidden.md"))).toBe(false);

  await to.fill("clash-first");
  await page.keyboard.press("Enter");
  await expect(dialog).toHaveCount(0);
  await expect.poll(onDisk("clash-first.md")).toBe("# First\n");
  expect(readFileSync(vault("clash.md"), "utf8")).toBe("# Second\n");
  await expect(page.getByRole("button", { name: "clash-first.md", exact: true })).toBeVisible();
});
