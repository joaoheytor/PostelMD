import { readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Page, Route } from "@playwright/test";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// Saves that fail or never answer, around logout (the pre-release review of 2026-11-10).

const vault = (name: string) => join(env("PM_E2E_VAULT"), name);

async function typeAtEnd(page: Page, name: string, text: string): Promise<void> {
  await page.getByRole("textbox", { name: `Text of ${name}` }).click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type(text);
}

const isSave = (r: Route) => r.request().method() === "PUT";
const saveStatus = (page: Page) => page.getByRole("status", { name: "Save status" });

/** page's saves answer 503 after a while, for good. */
async function busyServer(page: Page, afterMs = 0): Promise<void> {
  await page.route("**/api/v1/notes/**", async (r) => {
    if (!isSave(r)) return r.fallback();
    await new Promise((done) => setTimeout(done, afterMs));
    await r.fulfill({ status: 503, headers: { "Retry-After": "60" }, json: { error: "busy" } }).catch(() => {});
  });
}

test("logging out while a save fails asks first, and keeps the edits as a draft", async ({ page }) => {
  writeFileSync(vault("leaving.md"), "# Leaving\n");
  try {
    await login(page);
    await page.getByRole("button", { name: "leaving.md" }).click();
    await busyServer(page, 1500);
    const text = page.getByRole("textbox", { name: "Text of leaving.md" });
    await typeAtEnd(page, "leaving.md", "cordon the node");
    await expect(saveStatus(page)).toHaveText("Not saved: the server is busy. Retrying; your edits are kept in this browser.");

    // The save it tries first takes a while: the button says so at once, and keeps the focus so
    // the new label is read out; clicking it again does nothing.
    await page.getByRole("button", { name: "Log out" }).click();
    const saving = page.getByRole("button", { name: "Saving before you log out…" });
    await expect(saving).toBeDisabled();
    await expect(saving).toBeFocused();
    const ask = page.getByRole("dialog", { name: "Your edits to leaving.md aren't saved" });
    await expect(ask).toBeVisible();
    await expect(ask).toContainText("The server is busy.");
    await noSeriousA11yViolations(page);
    await ask.getByRole("button", { name: "Stay" }).click();
    await expect(ask).toBeHidden();
    await expect(text).toBeFocused();
    await expect(text).toContainText("cordon the node");

    // Escape stays too.
    await page.getByRole("button", { name: "Log out" }).click();
    await expect(ask).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(ask).toBeHidden();
    await expect(text).toBeFocused();

    await page.getByRole("button", { name: "Log out" }).click();
    await ask.getByRole("button", { name: "Log out and keep a draft" }).click();
    await expect(page.getByLabel("Password")).toBeVisible();
    expect(readFileSync(vault("leaving.md"), "utf8")).toBe("# Leaving\n"); // only the draft has it

    // Back in, the draft is offered on the note, and restoring it saves it.
    await page.unroute("**/api/v1/notes/**");
    await login(page);
    await page.getByRole("button", { name: "leaving.md" }).click();
    const banner = page.getByRole("region", { name: "This note has an unsaved draft" });
    await expect(banner).toBeVisible();
    page.once("dialog", (d) => void d.accept());
    await banner.getByRole("button", { name: "Restore draft" }).click();
    await expect.poll(() => readFileSync(vault("leaving.md"), "utf8")).toBe("# Leaving\ncordon the node");
  } finally {
    rmSync(vault("leaving.md"), { force: true });
  }
});

/**
 * Holds page's first save of a note unanswered until the test ends; later saves go through.
 * With landed, the first save reaches the server first: only its answer is lost.
 */
async function stallFirstSave(page: Page, landed: boolean): Promise<{ saves: () => number; release: () => Promise<void> }> {
  let saves = 0;
  let held: Route | undefined;
  let release: () => void = () => {};
  const done = new Promise<void>((r) => (release = r));
  await page.route("**/api/v1/notes/**", async (r) => {
    if (!isSave(r)) return r.fallback();
    if (++saves > 1) return r.continue();
    held = r;
    if (landed) await r.fetch();
    await done;
  });
  return {
    saves: () => saves,
    release: async () => {
      release();
      await held?.abort().catch(() => {}); // the browser has given up on it already
    },
  };
}

const stalls = [
  { name: "stuck.md", landed: false, more: "", what: "it hadn't landed" },
  // The retry sends the same bytes: the server answers 204 (ADR-0027).
  { name: "stuck-landed.md", landed: true, more: "", what: "it had landed" },
  // The retry sends other bytes: a 412 with the stalled bytes, which landed, is no conflict.
  { name: "stuck-typed.md", landed: true, more: " now", what: "it had landed, and the user typed on" },
];

for (const { name, landed, more, what } of stalls) {
  test(`a save that never answers says it is retrying, then saves (${what})`, async ({ page }) => {
    test.setTimeout(60_000); // the save gives up after 15 s
    writeFileSync(vault(name), "# Stuck\n");
    let stall: Awaited<ReturnType<typeof stallFirstSave>> | undefined;
    try {
      await login(page);
      await page.getByRole("button", { name }).click();
      stall = await stallFirstSave(page, landed);
      await typeAtEnd(page, name, "scale the deployment");
      const status = saveStatus(page);
      await expect(status).toHaveText("Saving…");
      if (more) await page.keyboard.type(more);
      await expect(status).toHaveText("Saving is taking too long; retrying", { timeout: 20_000 });
      if (!landed) expect(readFileSync(vault(name), "utf8")).toBe("# Stuck\n");
      await expect.poll(() => readFileSync(vault(name), "utf8")).toBe(`# Stuck\nscale the deployment${more}`);
      await expect(status).toHaveText("Saved");
      expect(stall.saves()).toBe(more ? 3 : 2);
      await expect(page.getByRole("button", { name: "Keep my version" })).toHaveCount(0);
    } finally {
      await stall?.release();
      rmSync(vault(name), { force: true });
    }
  });
}

test("when the browser refuses the draft, the editor says so and never claims the edits are kept", async ({ page }) => {
  writeFileSync(vault("no-room.md"), "# No room\n");
  try {
    // IndexedDB over its quota: every draft write throws.
    await page.addInitScript(() => {
      IDBObjectStore.prototype.put = () => {
        throw new DOMException("The quota has been exceeded.", "QuotaExceededError");
      };
    });
    await login(page);
    await page.getByRole("button", { name: "no-room.md" }).click();
    const copy = page.getByRole("status", { name: "Local copy" });
    await expect(copy).toHaveText(""); // there all along, so it is announced when it speaks
    await busyServer(page);
    await typeAtEnd(page, "no-room.md", "evict the pod");
    await expect(copy).toHaveText(/^Couldn't keep a local copy of your edits in this browser/);
    await expect(saveStatus(page)).toHaveText("Not saved: the server is busy. Retrying.");

    // Logging out doesn't offer a draft the browser is already refusing: Stay is the only choice.
    await page.getByRole("button", { name: "Log out" }).click();
    const ask = page.getByRole("dialog", { name: "Your edits to no-room.md aren't saved" });
    await expect(ask.getByRole("alert")).toContainText("Couldn't keep a local copy of your edits in this browser either.");
    await expect(ask.getByRole("button", { name: "Log out and keep a draft" })).toHaveCount(0);
    await expect(ask.getByRole("button", { name: "Stay" })).toBeFocused();
    await ask.getByRole("button", { name: "Stay" }).click();
    await expect(page.getByRole("textbox", { name: "Text of no-room.md" })).toContainText("evict the pod");
  } finally {
    rmSync(vault("no-room.md"), { force: true });
  }
});
