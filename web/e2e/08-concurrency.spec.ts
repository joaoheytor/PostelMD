import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Page, Request, Response } from "@playwright/test";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// The save protocol's races, in a real browser (ADR-0003 §7, docs/roadmap.md v0.1).

const vault = (name: string) => join(env("PM_E2E_VAULT"), name);

const isSaveOf = (name: string) => (r: Request) => r.method() === "PUT" && r.url().endsWith(`/api/v1/notes/${name}`);

/** The status codes of page's saves of name, in order. */
function saveStatuses(page: Page, name: string): number[] {
  const seen: number[] = [];
  page.on("response", (r: Response) => {
    if (isSaveOf(name)(r.request())) seen.push(r.status());
  });
  return seen;
}

async function typeAtEnd(page: Page, name: string, text: string): Promise<void> {
  await page.getByRole("textbox", { name: `Text of ${name}` }).click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type(text);
}

test("two tabs edit the same note: the second save is a conflict, and nothing is lost", async ({ page, context }) => {
  writeFileSync(vault("shared.md"), "# Shared\n");
  await login(page);
  await page.getByRole("button", { name: "shared.md" }).click();
  const tab2 = await context.newPage();
  await tab2.goto("/notes/shared.md");
  await expect(tab2.getByRole("textbox", { name: "Text of shared.md" })).toContainText("# Shared");
  const tab2Saves = saveStatuses(tab2, "shared.md");

  await typeAtEnd(page, "shared.md", "from tab one");
  await expect.poll(() => readFileSync(vault("shared.md"), "utf8")).toBe("# Shared\nfrom tab one");

  // Tab 2 still has the first version as its base.
  await typeAtEnd(tab2, "shared.md", "from tab two");
  const conflict = tab2
    .getByRole("alert")
    .filter({ has: tab2.getByRole("heading", { name: "This note changed in another tab or outside this editor" }) });
  await expect(conflict).toBeVisible();
  await expect(tab2.getByRole("status", { name: "Save status" })).toHaveText("Not saved: conflict");
  expect(tab2Saves).toEqual([412]);

  // Nothing is lost: tab 1's text is on disk, and tab 2 still has its own edit and shows tab 1's.
  expect(readFileSync(vault("shared.md"), "utf8")).toBe("# Shared\nfrom tab one");
  await expect(tab2.getByRole("textbox", { name: "Text of shared.md" })).toContainText("from tab two");
  // The banner shows what differs, and what each way out does.
  const diff = conflict.getByLabel("Changes from your text to the other version");
  await expect(diff.getByRole("deletion")).toHaveText("- from tab two");
  await expect(diff.getByRole("insertion")).toHaveText("+ from tab one");
  await expect(conflict.getByRole("button", { name: "Keep my version" })).toHaveAccessibleDescription(
    "saves your text over the other version.",
  );
  await expect(conflict.getByRole("button", { name: "Use the other version" })).toHaveAccessibleDescription(
    "replaces your text in the editor; Ctrl+Z brings it back.",
  );
  await noSeriousA11yViolations(tab2);

  // Autosave stays stopped until the user chooses.
  await typeAtEnd(tab2, "shared.md", " still");
  await tab2.keyboard.press("ControlOrMeta+S");
  await expect(tab2.getByRole("status", { name: "Save status" })).toHaveText("Not saved: conflict");
  expect(tab2Saves).toEqual([412]);
  expect(readFileSync(vault("shared.md"), "utf8")).toBe("# Shared\nfrom tab one");
  await tab2.close();
});

test("a slow save's response never marks newer edits as saved", async ({ page }) => {
  writeFileSync(vault("slow.md"), "# Slow\n");
  await login(page);
  await page.getByRole("button", { name: "slow.md" }).click();

  // The first save waits 2 s before it reaches the server; later ones go straight through.
  const bodies: string[] = [];
  let firstSent!: () => void;
  const inFlight = new Promise<void>((resolve) => (firstSent = resolve));
  await page.route("**/api/v1/notes/slow.md", async (route) => {
    if (route.request().method() !== "PUT") return route.fallback();
    bodies.push(route.request().postData() ?? "");
    if (bodies.length === 1) {
      firstSent();
      await new Promise((resolve) => setTimeout(resolve, 2000));
    }
    await route.fallback();
  });
  const firstResponse = page.waitForResponse((r) => isSaveOf("slow.md")(r.request()));

  await typeAtEnd(page, "slow.md", "alpha");
  await inFlight;
  await page.keyboard.type(" beta"); // while the save of "alpha" is in flight
  expect(bodies).toEqual(["# Slow\nalpha"]);

  expect((await firstResponse).status()).toBe(204);
  // The response is for "alpha" only: " beta" is still an unsaved edit.
  await expect(page.getByRole("status", { name: "Save status" })).toHaveText("Edited");
  expect(readFileSync(vault("slow.md"), "utf8")).toBe("# Slow\nalpha");

  // A second save sends it.
  await expect.poll(() => readFileSync(vault("slow.md"), "utf8")).toBe("# Slow\nalpha beta");
  expect(bodies).toEqual(["# Slow\nalpha", "# Slow\nalpha beta"]);
  await expect(page.getByRole("status", { name: "Save status" })).toHaveText("Saved");
});

test("a note deleted while its autosave is pending is not recreated", async ({ page }) => {
  writeFileSync(vault("ephemeral.md"), "# Ephemeral\n");
  await login(page);
  await page.getByRole("button", { name: "ephemeral.md" }).click();
  const saves = saveStatuses(page, "ephemeral.md");
  const sent: Request[] = [];
  page.on("request", (r) => void (isSaveOf("ephemeral.md")(r) && sent.push(r)));

  await typeAtEnd(page, "ephemeral.md", "one more line");
  rmSync(vault("ephemeral.md")); // within the autosave's debounce
  expect(sent).toHaveLength(0);

  const gone = page.getByRole("alert").filter({ has: page.getByRole("heading", { name: "This note was deleted or moved outside this editor" }) });
  await expect(gone).toBeVisible();
  await expect(page.getByRole("status", { name: "Save status" })).toHaveText("Not saved: the note is gone");
  expect(saves).toEqual([404]);
  await expect(page.getByRole("textbox", { name: "Text of ephemeral.md" })).toContainText("one more line");

  // More edits and an explicit save don't recreate it either.
  await typeAtEnd(page, "ephemeral.md", " and another");
  await page.keyboard.press("ControlOrMeta+S");
  await page.waitForTimeout(3000); // longer than the autosave debounce and the first retry
  expect(existsSync(vault("ephemeral.md"))).toBe(false);
  expect(saves).toEqual([404]);
  for (const r of sent) expect(await r.allHeaders()).not.toHaveProperty("if-none-match");
});
