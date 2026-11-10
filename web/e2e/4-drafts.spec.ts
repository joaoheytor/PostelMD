import { readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { BrowserContext, Page } from "@playwright/test";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

const vault = (name: string) => join(env("PM_E2E_VAULT"), name);

/**
 * page's saves fail as if the server were busy, for good: until one lands, the draft is the
 * only other copy. Other pages save normally.
 */
async function serverDown(context: BrowserContext, page: Page): Promise<void> {
  await context.route("**/api/v1/notes/**", (r) =>
    r.request().method() === "PUT" && r.request().frame().page() === page
      ? r.fulfill({ status: 503, headers: { "Retry-After": "60" }, json: { error: "busy" } })
      : r.fallback(),
  );
}

/**
 * Kills page's renderer, as a browser crash would: no unload or visibility handlers run, so
 * nothing more is saved. (Closing the page isn't a crash: its last save escapes the routes.)
 */
async function crash(context: BrowserContext, page: Page): Promise<void> {
  const crashed = page.waitForEvent("crash");
  const cdp = await context.newCDPSession(page);
  void cdp.send("Page.crash").catch(() => {}); // never answers: the page is gone
  await crashed;
  await page.close();
}

async function typeAtEnd(page: Page, name: string, text: string): Promise<void> {
  await page.getByRole("textbox", { name: `Text of ${name}` }).click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type(text);
}

test("an edit that never reached the server is recovered after its tab is gone", async ({ page, context }) => {
  writeFileSync(vault("drafty.md"), "# Drafty\n");
  await login(page);
  await page.getByRole("button", { name: "drafty.md" }).click();
  await serverDown(context, page);
  await typeAtEnd(page, "drafty.md", "restart the pod");
  await expect(page.getByRole("status")).toHaveText(/^Not saved/);
  await crash(context, page);
  expect(readFileSync(vault("drafty.md"), "utf8")).toBe("# Drafty\n"); // only the draft has it

  const again = await context.newPage();
  await again.goto("/notes/drafty.md");
  const banner = again.getByRole("region", { name: "This note has an unsaved draft" });
  await expect(banner).toBeVisible();
  await banner.getByRole("button", { name: "Compare" }).click();
  await expect(banner.getByLabel("Changes from the current text to the draft")).toContainText("+ restart the pod");
  await noSeriousA11yViolations(again);
  again.once("dialog", (d) => void d.accept());
  await banner.getByRole("button", { name: "Restore draft" }).click();
  await expect(again.getByRole("textbox", { name: "Text of drafty.md" })).toContainText("restart the pod");
  // It saves through the normal path. (The status read "Saved" before, too: wait for the file.)
  await expect.poll(() => readFileSync(vault("drafty.md"), "utf8")).toBe("# Drafty\nrestart the pod");
  await expect(again.getByRole("status")).toHaveText("Saved");
  await expect(banner).toBeHidden();

  // Saved, so nothing is left to offer.
  await again.close();
  const third = await context.newPage();
  await third.goto("/notes/drafty.md");
  await expect(third.getByRole("textbox", { name: "Text of drafty.md" })).toContainText("restart the pod");
  await expect(third.getByRole("region", { name: /unsaved draft/ })).toHaveCount(0);
  await expect(third.getByRole("button", { name: /Unsaved drafts/ })).toHaveCount(0);
});

test("a reload offers the draft the page had, instead of writing over it", async ({ page, context }) => {
  writeFileSync(vault("reload.md"), "# Reload\n");
  await login(page);
  await page.getByRole("button", { name: "reload.md" }).click();
  await serverDown(context, page);
  await typeAtEnd(page, "reload.md", "drain the node");
  await expect(page.getByRole("status")).toHaveText(/^Not saved/);
  // The reload flushes the edit, as it should; a request sent while the page navigates can
  // escape the routes (#87). Changing the note first makes such a save fail its If-Match
  // with 412 instead of landing, so only the draft can hold the edit.
  const elsewhere = "# Reload\nedited elsewhere\n";
  writeFileSync(vault("reload.md"), elsewhere);
  page.on("dialog", (d) => void d.accept()); // "leave the page?", then "discard the draft?"
  await page.reload();
  await expect(page.getByRole("textbox", { name: "Text of reload.md" })).toContainText("edited elsewhere");
  const banner = page.getByRole("region", { name: "This note has an unsaved draft" });
  await expect(banner).toBeVisible();
  await banner.getByRole("button", { name: "Compare" }).click();
  await expect(banner.getByLabel("Changes from the current text to the draft")).toContainText("+ drain the node");
  await banner.getByRole("button", { name: "Discard draft" }).click();
  await expect(banner).toBeHidden();
  await expect(page.getByRole("button", { name: /Unsaved drafts/ })).toHaveCount(0);
  expect(readFileSync(vault("reload.md"), "utf8")).toBe(elsewhere);
});

test("the draft of a note deleted meanwhile can be saved as a new note", async ({ page, context }) => {
  writeFileSync(vault("doomed.md"), "# Doomed\n");
  await login(page);
  await page.getByRole("button", { name: "doomed.md" }).click();
  await serverDown(context, page);
  await typeAtEnd(page, "doomed.md", "last words");
  await expect(page.getByRole("status")).toHaveText(/^Not saved/);
  await crash(context, page);
  rmSync(vault("doomed.md"));

  const again = await context.newPage();
  await again.goto("/");
  await again.getByRole("button", { name: "Unsaved drafts (1)" }).click();
  const dialog = again.getByRole("dialog", { name: "Unsaved drafts" });
  await expect(dialog.getByRole("list", { name: "Drafts" })).toContainText("doomed.md");
  await noSeriousA11yViolations(again);
  await dialog.getByRole("button", { name: "Save as new note" }).click();
  await expect(again.getByRole("textbox", { name: "Text of doomed-recovered.md" })).toContainText("last words");
  expect(readFileSync(vault("doomed-recovered.md"), "utf8")).toBe("# Doomed\nlast words");
  await expect(again.getByRole("button", { name: /Unsaved drafts/ })).toHaveCount(0);
});
