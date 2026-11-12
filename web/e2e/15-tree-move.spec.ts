import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import type { Locator, Page } from "@playwright/test";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// Moving notes and folders in the file tree by drag and drop, or with "Move to…" (#130). The
// server's move never overwrites (ADR-0022); the tree only chooses where the item goes.

const vault = (...p: string[]) => join(env("PM_E2E_VAULT"), ...p);
const onDisk = (p: string) => () => (existsSync(vault(p)) ? readFileSync(vault(p), "utf8") : "<missing>");

/** Every top-level item this spec creates: removed after each test, whatever happened. */
const made = ["dnd-note.md", "dnd-target", "dnd-clash.md", "dnd-taken", "dnd-parent", "dnd-child", "dnd-open.md", "dnd-dest", "dnd-kb.md", "dnd-kb-dest", "dnd-kb-folder"];

/** Notes that make the tree taller than the window, as in a real vault: drags have to scroll. */
const padding = Array.from({ length: 80 }, (_, i) => `dnd-pad-${String(i).padStart(2, "0")}.md`);

test.beforeAll(() => {
  for (const p of padding) writeFileSync(vault(p), "# Padding\n");
});

test.afterAll(() => {
  for (const p of padding) rmSync(vault(p), { force: true });
});

test.afterEach(() => {
  for (const p of made) rmSync(vault(p), { recursive: true, force: true });
});

function put(path: string, text: string): void {
  mkdirSync(dirname(vault(path)), { recursive: true });
  writeFileSync(vault(path), text);
}

const tree = (page: Page) => page.getByRole("navigation", { name: "Notes" });
/** A folder's entry in the tree: its name follows the ▸ or ▾ marker. */
const treeFolder = (page: Page, name: string) => tree(page).getByRole("list").getByRole("button", { name: new RegExp(`^\\S ${name}$`) });
const treeNote = (page: Page, name: string) => tree(page).getByRole("button", { name, exact: true });
const treeStatus = (page: Page) => page.getByRole("status", { name: "File tree status" });

/**
 * Drags from onto to with the mouse. locator.dragTo drops nothing when the target has to be
 * scrolled into view during the drag; here the drag starts, the tree scrolls, then the drop.
 */
async function drag(page: Page, from: Locator, to: Locator, at?: { x: number; y: number }): Promise<void> {
  await from.hover({ position: { x: 4, y: 4 } });
  await page.mouse.down();
  const box = await from.boundingBox();
  if (box) await page.mouse.move(box.x + 12, box.y + 12, { steps: 2 }); // the drag starts
  await to.scrollIntoViewIfNeeded();
  await to.hover({ position: at ?? { x: 10, y: 8 } });
  await to.hover({ position: at ? { x: at.x + 2, y: at.y } : { x: 12, y: 8 } }); // a dragover after the scroll
  await page.mouse.up();
}

test("drag a note onto a folder, and back onto the vault", async ({ page }) => {
  put("dnd-note.md", "# Drag me\n");
  mkdirSync(vault("dnd-target"));
  await login(page);

  await drag(page, treeNote(page, "dnd-note.md"), treeFolder(page, "dnd-target"));
  await expect.poll(onDisk("dnd-target/dnd-note.md")).toBe("# Drag me\n");
  expect(existsSync(vault("dnd-note.md"))).toBe(false);
  await expect(treeStatus(page)).toHaveText("Moved dnd-note.md to dnd-target/dnd-note.md.");

  // Onto its own folder (shown expanded after the move): nothing happens, nothing is announced.
  const root = page.getByRole("button", { name: "Vault", exact: true });
  await root.click(); // clears the status
  await expect(treeStatus(page)).toHaveText("");
  await drag(page, treeNote(page, "dnd-note.md"), treeFolder(page, "dnd-target"));
  await noSeriousA11yViolations(page);
  await drag(page, treeNote(page, "dnd-note.md"), root);
  await expect(treeStatus(page)).toHaveText("Moved dnd-target/dnd-note.md to dnd-note.md."); // the first drop did nothing
  await expect.poll(onDisk("dnd-note.md")).toBe("# Drag me\n");
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("dropping onto a folder that has that name already changes nothing and says why", async ({ page }) => {
  put("dnd-clash.md", "# Mine\n");
  put("dnd-taken/dnd-clash.md", "# Theirs\n");
  await login(page);

  await drag(page, treeNote(page, "dnd-clash.md"), treeFolder(page, "dnd-taken"));
  await expect(page.getByRole("alert")).toContainText("Couldn't move dnd-clash.md: dnd-taken/dnd-clash.md already exists");
  expect(readFileSync(vault("dnd-clash.md"), "utf8")).toBe("# Mine\n");
  expect(readFileSync(vault("dnd-taken/dnd-clash.md"), "utf8")).toBe("# Theirs\n");
  await expect(treeStatus(page)).toHaveText("");
});

test("a folder can't be dropped into itself or its own subfolder", async ({ page }) => {
  mkdirSync(vault("dnd-parent", "dnd-child"), { recursive: true });
  await login(page);
  await treeFolder(page, "dnd-parent").click(); // expands it

  await drag(page, treeFolder(page, "dnd-parent"), treeFolder(page, "dnd-child"));
  // Onto itself: a drag across its own entry (from one end to the other, so it isn't a click).
  await drag(page, treeFolder(page, "dnd-parent"), treeFolder(page, "dnd-parent"), { x: 60, y: 8 });
  await expect(treeFolder(page, "dnd-child")).toBeVisible(); // still expanded: no click toggled it
  // A drop that is allowed, so the two above had their chance to send a move first.
  await drag(page, treeFolder(page, "dnd-child"), page.getByRole("button", { name: "Vault", exact: true }));
  await expect(treeStatus(page)).toHaveText("Moved dnd-parent/dnd-child to dnd-child.");
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect(existsSync(vault("dnd-child"))).toBe(true);
  expect(existsSync(vault("dnd-parent"))).toBe(true);
});

test("a file dragged in from outside the app is not a move", async ({ page }) => {
  put("dnd-note.md", "# Stay\n");
  mkdirSync(vault("dnd-target"));
  await login(page);

  // A desktop file, and a drag that claims to be the tree's: neither is accepted as a drop.
  const accepted = await treeFolder(page, "dnd-target").evaluate((el) =>
    [(dt: DataTransfer) => dt.items.add(new File(["# x\n"], "x.md")), (dt: DataTransfer) => dt.setData("application/x-postelmd-item", "dnd-note.md")].map((fill) => {
      const dt = new DataTransfer();
      fill(dt);
      const over = !el.dispatchEvent(new DragEvent("dragover", { dataTransfer: dt, bubbles: true, cancelable: true }));
      const drop = !el.dispatchEvent(new DragEvent("drop", { dataTransfer: dt, bubbles: true, cancelable: true }));
      return over || drop;
    }),
  );
  expect(accepted).toEqual([false, false]);
  expect(readFileSync(vault("dnd-note.md"), "utf8")).toBe("# Stay\n");
  expect(existsSync(vault("dnd-target", "dnd-note.md"))).toBe(false);
});

test("the open note follows a drag with its unsaved edit", async ({ page }) => {
  put("dnd-open.md", "# Open\n");
  mkdirSync(vault("dnd-dest"));
  await login(page);
  await treeNote(page, "dnd-open.md").click();
  const text = page.getByRole("textbox", { name: "Text of dnd-open.md" });
  await text.press("ControlOrMeta+End");
  await text.pressSequentially("Typed before the move.");
  await expect(page.getByRole("status", { name: "Save status" })).not.toHaveText("Saved");

  await drag(page, treeNote(page, "dnd-open.md"), treeFolder(page, "dnd-dest"));
  const moved = page.getByRole("textbox", { name: "Text of dnd-dest/dnd-open.md" });
  await expect(moved).toContainText("Typed before the move.");
  await expect(page).toHaveURL(/\/notes\/dnd-dest\/dnd-open\.md$/);
  await expect.poll(onDisk("dnd-dest/dnd-open.md")).toBe("# Open\nTyped before the move.");
  expect(existsSync(vault("dnd-open.md"))).toBe(false);

  // Still the same editor session: the next edit saves at the new path.
  await moved.press("ControlOrMeta+End");
  await moved.pressSequentially(" And after.");
  await expect.poll(onDisk("dnd-dest/dnd-open.md")).toBe("# Open\nTyped before the move. And after.");
});

test("Move to… moves the open note, and Move folder to… a folder, from the keyboard", async ({ page }) => {
  put("dnd-kb.md", "# Keys\n");
  mkdirSync(vault("dnd-kb-dest"));
  mkdirSync(vault("dnd-kb-folder", "inside"), { recursive: true });
  await login(page);
  await treeNote(page, "dnd-kb.md").click();

  const moveTo = page.getByRole("button", { name: "Move to…" });
  await moveTo.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: "Move dnd-kb.md" });
  const into = dialog.getByLabel("Into the folder");
  await expect(into).toBeFocused();
  await noSeriousA11yViolations(page);
  await into.selectOption("dnd-kb-dest");
  await page.keyboard.press("Tab");
  await expect(dialog.getByRole("button", { name: "Move", exact: true })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "Text of dnd-kb-dest/dnd-kb.md" })).toBeFocused();
  await expect(treeStatus(page)).toHaveText("Moved dnd-kb.md to dnd-kb-dest/dnd-kb.md.");
  await expect.poll(onDisk("dnd-kb-dest/dnd-kb.md")).toBe("# Keys\n");

  // A folder: it isn't offered itself or its subfolders; Escape closes the dialog unchanged.
  await treeFolder(page, "dnd-kb-folder").click();
  const moveFolder = page.getByRole("button", { name: "Move folder to…" });
  await moveFolder.click();
  const folderDialog = page.getByRole("dialog", { name: "Move dnd-kb-folder" });
  const options = await folderDialog.getByLabel("Into the folder").getByRole("option").allTextContents();
  expect(options).toContain("dnd-kb-dest");
  expect(options.filter((o) => o.startsWith("dnd-kb-folder"))).toEqual([]);
  expect(options).not.toContain("Vault (top level)"); // its own folder
  await page.keyboard.press("Escape");
  await expect(folderDialog).toHaveCount(0);
  await expect(moveFolder).toBeFocused();

  await moveFolder.click();
  await folderDialog.getByLabel("Into the folder").selectOption("dnd-kb-dest");
  await folderDialog.getByRole("button", { name: "Move", exact: true }).click();
  await expect(treeStatus(page)).toHaveText("Moved dnd-kb-folder to dnd-kb-dest/dnd-kb-folder.");
  expect(existsSync(vault("dnd-kb-dest", "dnd-kb-folder", "inside"))).toBe(true);
  expect(existsSync(vault("dnd-kb-folder"))).toBe(false);
});
