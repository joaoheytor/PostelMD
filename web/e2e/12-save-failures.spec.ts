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

test("logging out while a save fails asks first, and keeps the edits as a draft", async ({ page }) => {
  writeFileSync(vault("leaving.md"), "# Leaving\n");
  try {
    await login(page);
    await page.getByRole("button", { name: "leaving.md" }).click();
    await page.route("**/api/v1/notes/**", (r) =>
      isSave(r) ? r.fulfill({ status: 503, headers: { "Retry-After": "60" }, json: { error: "busy" } }) : r.fallback(),
    );
    await typeAtEnd(page, "leaving.md", "cordon the node");
    await expect(page.getByRole("status")).toHaveText(/^Not saved/);

    await page.getByRole("button", { name: "Log out" }).click();
    const ask = page.getByRole("dialog", { name: "Your edits to leaving.md aren't saved" });
    await expect(ask).toBeVisible();
    await expect(ask).toContainText("the server is busy");
    await noSeriousA11yViolations(page);
    await ask.getByRole("button", { name: "Stay" }).click();
    await expect(ask).toBeHidden();
    await expect(page.getByRole("textbox", { name: "Text of leaving.md" })).toContainText("cordon the node");

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

for (const landed of [false, true]) {
  test(`a save that never answers says it is retrying, then saves (${landed ? "it had landed" : "it hadn't landed"})`, async ({ page }) => {
    test.setTimeout(60_000); // the save gives up after 15 s
    const name = landed ? "stuck-landed.md" : "stuck.md";
    writeFileSync(vault(name), "# Stuck\n");
    let stall: Awaited<ReturnType<typeof stallFirstSave>> | undefined;
    try {
      await login(page);
      await page.getByRole("button", { name }).click();
      stall = await stallFirstSave(page, landed);
      await typeAtEnd(page, name, "scale the deployment");
      const status = page.getByRole("status");
      await expect(status).toHaveText("Saving…");
      await expect(status).toHaveText("Saving is taking too long; retrying", { timeout: 20_000 });
      if (!landed) expect(readFileSync(vault(name), "utf8")).toBe("# Stuck\n");
      await expect.poll(() => readFileSync(vault(name), "utf8")).toBe("# Stuck\nscale the deployment");
      await expect(status).toHaveText("Saved");
      expect(stall.saves()).toBe(2);
      // A retry of bytes that had landed is answered like a save (ADR-0027): no conflict.
      await expect(page.getByRole("button", { name: "Keep my version" })).toHaveCount(0);
    } finally {
      await stall?.release();
      rmSync(vault(name), { force: true });
    }
  });
}
