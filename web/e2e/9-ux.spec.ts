import AxeBuilder from "@axe-core/playwright";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { env, expect, login, noSeriousA11yViolations, password, test } from "./helpers";

// What a first user runs into: where focus goes, what errors say, and where the open note is.

const vault = (...p: string[]) => join(env("PM_E2E_VAULT"), ...p);

test("New note takes the focus: type a name, Enter, and the new note is open to type in", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "Vault" }).click();
  await page.getByRole("button", { name: "New note" }).click();
  await expect(page.getByLabel("Note name in the vault")).toBeFocused();
  await page.keyboard.type("typed-name");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("textbox", { name: "Text of typed-name.md" })).toBeFocused();
  expect(existsSync(vault("typed-name.md"))).toBe(true);

  // Escape closes the form, and the focus goes back to the button that opened it.
  await page.getByRole("button", { name: "New folder" }).click();
  await expect(page.getByLabel("Folder name in the vault")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "New folder" })).toBeFocused();
});

test("rename by keyboard: the field starts focused with the path selected", async ({ page }) => {
  writeFileSync(vault("keys.md"), "# Keys\n");
  await login(page);
  await page.getByRole("button", { name: "keys.md" }).click();
  await page.getByRole("button", { name: "Rename or move" }).click();
  const field = page.getByLabel("New path for keys.md");
  await expect(field).toBeFocused();
  await page.keyboard.type("renamed-by-keys"); // replaces the selected path
  await page.keyboard.press("Enter");
  // The moved note's editor has the focus, as after New note.
  await expect(page.getByRole("textbox", { name: "Text of renamed-by-keys.md" })).toBeFocused();
  await expect.poll(() => existsSync(vault("renamed-by-keys.md"))).toBe(true);

  await page.getByRole("button", { name: "Rename or move" }).click();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Rename or move" })).toBeFocused();
});

test("the editor screen has one main heading, with the preview hidden", async ({ page }) => {
  writeFileSync(vault("headings.md"), "# Headings\n");
  await login(page);
  await page.getByRole("button", { name: "headings.md" }).click();
  await expect(page.getByRole("textbox", { name: "Text of headings.md" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Preview", exact: true })).toHaveAttribute("aria-pressed", "false");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("PostelMD");
  await expect(page.getByRole("heading", { level: 2, name: "headings.md" })).toBeVisible();
  const { violations } = await new AxeBuilder({ page }).withRules(["page-has-heading-one", "heading-order"]).analyze();
  expect(violations).toEqual([]);
  await noSeriousA11yViolations(page);
});

test("moving into a folder that doesn't exist says so and keeps what was typed", async ({ page }) => {
  writeFileSync(vault("rotate-tls.md"), "# Rotate TLS\n");
  await login(page);
  await page.getByRole("button", { name: "rotate-tls.md" }).click();
  await page.getByRole("button", { name: "Rename or move" }).click();
  const field = page.getByLabel("New path for rotate-tls.md");
  await field.fill("runbooks/tls/rotate-tls.md");
  await field.press("Enter");

  await expect(page.getByRole("alert")).toContainText("Folder runbooks/tls doesn't exist. Create it first with New folder.");
  await expect(field).toHaveValue("runbooks/tls/rotate-tls.md");
  await expect(field).toBeFocused();
  expect(existsSync(vault("rotate-tls.md"))).toBe(true);
});

test("after Delete, the focus goes to the note's folder in the tree", async ({ page }) => {
  writeFileSync(vault("delete-me.md"), "# Delete me\n");
  await login(page);
  await page.getByRole("button", { name: "delete-me.md" }).click();
  page.once("dialog", (d) => void d.accept());
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.getByRole("button", { name: "delete-me.md" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Vault" })).toBeFocused();
});

test("closing a dialog puts the focus back on the button that opened it", async ({ page }) => {
  writeFileSync(vault("dialogs.md"), "# Dialogs\n");
  await login(page);
  await page.getByRole("button", { name: "dialogs.md" }).click();

  await page.getByRole("button", { name: "Commit…" }).click();
  await page.getByRole("dialog", { name: "Commit changes" }).getByRole("button", { name: "Close" }).click();
  await expect(page.getByRole("button", { name: "Commit…" })).toBeFocused();

  await page.getByRole("button", { name: "History" }).click();
  await page.getByRole("dialog", { name: "History of dialogs.md" }).getByRole("button", { name: "Close" }).click();
  await expect(page.getByRole("button", { name: "History" })).toBeFocused();

  await page.getByRole("button", { name: "Find a note" }).click();
  await page.getByRole("combobox", { name: "Note path" }).press("Escape");
  await expect(page.getByRole("button", { name: "Find a note" })).toBeFocused();
});

test("when the server can't be reached, the status says so in plain words", async ({ page, context }) => {
  writeFileSync(vault("unreachable.md"), "# Unreachable\n");
  await login(page);
  await page.getByRole("button", { name: "unreachable.md" }).click();
  await context.route("**/api/v1/notes/**", (r) => (r.request().method() === "PUT" ? r.abort("internetdisconnected") : r.fallback()));
  await page.getByRole("textbox", { name: "Text of unreachable.md" }).click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type("no network");
  await expect(page.getByRole("status")).toHaveText("Not saved: can't reach the server. Retrying; your edits are kept in this browser.");
  await expect(page.getByRole("status")).not.toContainText("TypeError");

  // Back online: the retry saves it.
  await context.unroute("**/api/v1/notes/**");
  await expect.poll(() => readFileSync(vault("unreachable.md"), "utf8"), { timeout: 15_000 }).toBe("# Unreachable\nno network");
});

test("the tree shows the open note, however it was opened", async ({ page }) => {
  mkdirSync(vault("deep", "er"), { recursive: true });
  writeFileSync(vault("deep", "er", "linked.md"), "# Linked\n");
  writeFileSync(vault("deep", "er", "found.md"), "# Found\n");
  // Enough notes before deep/ that it starts below the bottom of the tree.
  for (let i = 0; i < 60; i++) writeFileSync(vault(`c-pad-${String(i).padStart(2, "0")}.md`), "# Pad\n");

  // A deep link.
  await login(page);
  await page.goto("/notes/deep/er/linked.md");
  const tree = page.getByRole("navigation", { name: "Notes" });
  const deep = tree.getByRole("button", { name: /\bdeep$/ }); // a folder's name starts with its ▸ or ▾
  const er = tree.getByRole("button", { name: /\ber$/ });
  await expect(deep).toHaveAttribute("aria-expanded", "true");
  await expect(er).toHaveAttribute("aria-expanded", "true");
  await expect(tree.getByRole("button", { name: "linked.md" })).toHaveAttribute("aria-current", "page");
  await expect(tree.getByRole("button", { name: "linked.md" })).toBeInViewport();

  // Ctrl+P, after the folders were collapsed.
  await deep.click();
  await expect(er).toHaveCount(0);
  await page.keyboard.press("ControlOrMeta+P");
  await page.getByRole("combobox", { name: "Note path" }).fill("deep/er/found");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("textbox", { name: "Text of deep/er/found.md" })).toBeVisible();
  await expect(tree.getByRole("button", { name: "found.md" })).toHaveAttribute("aria-current", "page");
  await expect(tree.getByRole("button", { name: "linked.md" })).toHaveAttribute("aria-current", "false");
});

test("after several wrong passwords, the login says how long to wait", async ({ page }) => {
  await page.goto("/");
  const field = page.getByLabel("Password");
  const alert = page.getByRole("alert");
  // Five free failures, then each one waits twice as long as the last (ADR-0005).
  for (let i = 0; i < 8; i++) {
    await field.fill(`wrong ${i}`);
    await page.getByRole("button", { name: "Log in" }).click();
    await expect(alert).toHaveText(/^(Wrong password\.|Too many attempts\. Try again in \d+ s\.)$/);
  }
  await expect(alert).toHaveText(/^Too many attempts\. Try again in \d+ s\.$/);
  await expect(alert).toHaveCSS("color", "rgb(179, 38, 30)"); // --danger, not the card's muted grey
  await expect(page.getByRole("main")).toBeVisible();
  await noSeriousA11yViolations(page);

  // Logging in once the wait is over resets it, for the tests after this one.
  await expect(async () => {
    await field.fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await expect(page.getByRole("navigation", { name: "Notes" })).toBeVisible({ timeout: 1000 });
  }).toPass({ timeout: 30_000, intervals: [1000] });
});
