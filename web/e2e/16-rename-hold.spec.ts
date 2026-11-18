import { rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { env, expect, login, test } from "./helpers";

// A note copied to a new name, then deleted, outside PostelMD (ADR-0031's rule 2): the new
// path's first read waits for the old one to be missing long enough (SecondPass, 10 s), and
// the server answers 503 with Retry-After meanwhile. The web app waits it out and opens the
// note, instead of showing "the server is busy".

const vault = (name: string) => join(env("PM_E2E_VAULT"), name);

test("opening a note held by a rename waits for the server, then opens it", async ({ page }) => {
  test.setTimeout(45_000);
  const text = "# Held by a rename\n\nCopied, then the original deleted.\n";
  writeFileSync(vault("hold-before.md"), text);
  try {
    await login(page);
    // Its first read gives the original an ID.
    expect((await page.request.get("/api/v1/notes/hold-before.md")).status()).toBe(200);
    writeFileSync(vault("hold-after.md"), text); // a copy: another inode, the same bytes
    rmSync(vault("hold-before.md"));
    await page.reload();
    const answers: number[] = [];
    page.on("response", (r) => {
      if (r.url().endsWith("/api/v1/notes/hold-after.md")) answers.push(r.status());
    });
    await page.getByRole("button", { name: "hold-after.md" }).click();
    const editor = page.getByRole("textbox", { name: "Text of hold-after.md" });
    await expect(editor).toContainText("Copied, then the original deleted.", { timeout: 25_000 });
    expect(answers[0]).toBe(503); // held: the read waited
    expect(answers.at(-1)).toBe(200);
    await expect(page.getByRole("alert")).toHaveCount(0);
  } finally {
    rmSync(vault("hold-before.md"), { force: true });
    rmSync(vault("hold-after.md"), { force: true });
  }
});
