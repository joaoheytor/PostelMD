import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import type { Locator, Page } from "@playwright/test";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// Moving notes and folders in the file tree by drag and drop, or with the Folder list of the
// Rename or move form (#130). The server's move never overwrites (ADR-0022); the tree only
// chooses where the item goes.

const vault = (...p: string[]) => join(env("PM_E2E_VAULT"), ...p);
const onDisk = (p: string) => () => (existsSync(vault(p)) ? readFileSync(vault(p), "utf8") : "<missing>");

/** Every top-level item this spec creates: removed after each test, whatever happened. */
const made = ["dnd-note.md", "dnd-target", "dnd-clash.md", "dnd-taken", "dnd-parent", "dnd-child", "dnd-open.md", "dnd-dest", "dnd-kb.md", "dnd-kb-dest", "dnd-kb-folder", "dnd-kb-taken"];

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
  await expect(treeStatus(page)).toHaveText("Moved dnd-target/dnd-note.md to the vault."); // the first drop did nothing
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
  await expect(treeStatus(page)).toHaveText(""); // neither drop was taken, so nothing is announced
  // A drop that is allowed, so the two above had their chance to send a move first.
  await drag(page, treeFolder(page, "dnd-child"), page.getByRole("button", { name: "Vault", exact: true }));
  await expect(treeStatus(page)).toHaveText("Moved dnd-parent/dnd-child to the vault.");
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

  // The edit's save is held until the move is sent, or 3 s: a move that doesn't wait for the
  // save comes before its release here; one that waits comes after.
  const order: string[] = [];
  let moveSent = () => {};
  const moving = new Promise<void>((resolve) => (moveSent = resolve));
  await page.route("**/api/v1/notes/dnd-open.md", async (route) => {
    if (route.request().method() !== "PUT") return route.fallback();
    order.push("save sent");
    let timer: NodeJS.Timeout | undefined;
    await Promise.race([moving, new Promise((resolve) => (timer = setTimeout(resolve, 3000)))]);
    clearTimeout(timer);
    order.push("save released");
    await route.continue();
  });
  await page.route("**/api/v1/move", async (route) => {
    order.push("move");
    moveSent();
    await route.continue();
  });

  await drag(page, treeNote(page, "dnd-open.md"), treeFolder(page, "dnd-dest"));
  await expect.poll(() => order.at(-1)).toBe("move");
  expect(order[0]).toBe("save sent");
  expect(order.indexOf("move")).toBeGreaterThan(order.lastIndexOf("save released"));
  const moved = page.getByRole("textbox", { name: "Text of dnd-dest/dnd-open.md" });
  await expect(moved).toContainText("Typed before the move.");
  await expect(page).toHaveURL(/\/notes\/dnd-dest\/dnd-open\.md$/);
  await expect.poll(onDisk("dnd-dest/dnd-open.md")).toBe("# Open\nTyped before the move.");
  expect(existsSync(vault("dnd-open.md"))).toBe(false);

  // The note was reopened at its new path: the next edit saves there.
  await moved.press("ControlOrMeta+End");
  await moved.pressSequentially(" And after.");
  await expect.poll(onDisk("dnd-dest/dnd-open.md")).toBe("# Open\nTyped before the move. And after.");
});

const folderHelp = "Only folders open in the tree are listed. To go deeper, open the folder in the tree, or type a path.";

test("the open note's Rename or move form picks a folder; a failed move keeps the form", async ({ page }) => {
  put("dnd-kb.md", "# Keys\n");
  mkdirSync(vault("dnd-kb-dest"));
  put("dnd-kb-taken/dnd-kb.md", "# Taken\n");
  await login(page);
  await treeNote(page, "dnd-kb.md").click();

  await page.getByRole("button", { name: "Rename or move" }).focus();
  await page.keyboard.press("Enter");
  const path = page.getByLabel("New path for dnd-kb.md");
  const folder = page.getByLabel("Folder", { exact: true });
  await expect(path).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(folder).toBeFocused();
  await expect(folder).toHaveAccessibleDescription(folderHelp);
  await expect(folder).toHaveValue(""); // the note's own folder: the vault
  await noSeriousA11yViolations(page);

  // The list follows what is typed, so picking the vault after typing a folder works.
  await path.fill("dnd-kb-dest/dnd-kb.md");
  await expect(folder).toHaveValue("dnd-kb-dest");
  await folder.selectOption("");
  await expect(path).toHaveValue("dnd-kb.md");
  await folder.focus(); // back on the list, as after the Tab above

  // The name is taken there: the error shows once, and the form stays with what was picked,
  // though the note was closed and reopened for the move.
  await folder.selectOption("dnd-kb-taken");
  await expect(path).toHaveValue("dnd-kb-taken/dnd-kb.md");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toContainText("Couldn't move dnd-kb.md: dnd-kb-taken/dnd-kb.md already exists");
  await expect(page.getByRole("alert")).toHaveCount(1);
  await expect(path).toBeFocused();
  await expect(path).toHaveValue("dnd-kb-taken/dnd-kb.md");
  await expect(folder).toHaveValue("dnd-kb-taken");
  expect(readFileSync(vault("dnd-kb-taken/dnd-kb.md"), "utf8")).toBe("# Taken\n");

  // Another folder: it moves, the earlier error goes, and the editor gets the focus.
  await folder.selectOption("dnd-kb-dest");
  await page.getByRole("button", { name: "Move", exact: true }).click();
  await expect(page.getByRole("textbox", { name: "Text of dnd-kb-dest/dnd-kb.md" })).toBeFocused();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(treeStatus(page)).toHaveText("Moved dnd-kb.md to dnd-kb-dest/dnd-kb.md.");
  await expect.poll(onDisk("dnd-kb-dest/dnd-kb.md")).toBe("# Keys\n");
});

test("Rename or move folder from the keyboard: the focus after a failed and a done move", async ({ page }) => {
  mkdirSync(vault("dnd-kb-folder", "inside"), { recursive: true });
  mkdirSync(vault("dnd-kb-dest"));
  mkdirSync(vault("dnd-kb-taken", "dnd-kb-folder"), { recursive: true });
  await login(page);

  await treeFolder(page, "dnd-kb-folder").focus();
  await page.keyboard.press("Enter"); // selects it, and lists inside
  const opener = page.getByRole("button", { name: "Rename or move folder" });
  await opener.focus();
  await page.keyboard.press("Enter");
  const path = page.getByLabel("New path for dnd-kb-folder");
  const folder = page.getByLabel("Folder", { exact: true });
  await expect(path).toBeFocused();
  const options = await folder.getByRole("option").allTextContents();
  expect(options).toContain("Vault"); // where it is now
  expect(options).toContain("dnd-kb-dest");
  expect(options.filter((o) => o.startsWith("dnd-kb-folder"))).toEqual([]); // not itself, not inside

  await page.keyboard.press("Tab");
  await folder.selectOption("dnd-kb-taken");
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Move", exact: true })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toContainText("dnd-kb-taken/dnd-kb-folder already exists");
  await expect(path).toBeFocused(); // not the page: the field to fix

  await page.keyboard.press("Tab");
  await folder.selectOption("dnd-kb-dest");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Enter");
  await expect(treeStatus(page)).toHaveText("Moved dnd-kb-folder to dnd-kb-dest/dnd-kb-folder.");
  await expect(opener).toBeFocused(); // back on the button that opened the form
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect(existsSync(vault("dnd-kb-dest", "dnd-kb-folder", "inside"))).toBe(true);
  expect(existsSync(vault("dnd-kb-folder"))).toBe(false);
});
