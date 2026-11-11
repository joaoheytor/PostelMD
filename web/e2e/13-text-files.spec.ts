import { chmodSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Page } from "@playwright/test";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// Files that aren't Markdown: they open as text with the notes' save path, byte for byte, and
// a file the editor can't hold stays closed and says why.

const vault = (name: string) => join(env("PM_E2E_VAULT"), name);
const bom = Buffer.from([0xef, 0xbb, 0xbf]);

async function typeAtEnd(page: Page, name: string, text: string): Promise<void> {
  await page.getByRole("textbox", { name: `Text of ${name}` }).click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type(text);
}

test("a YAML file with a byte order mark and CRLF lines saves its edit and keeps both", async ({ page }) => {
  writeFileSync(vault("deploy.yaml"), Buffer.concat([bom, Buffer.from("apiVersion: v1\r\nkind: Pod\r\n")]));
  await login(page);
  await page.getByRole("button", { name: "deploy.yaml" }).click();
  await expect(page.getByRole("textbox", { name: "Text of deploy.yaml" })).toContainText("kind: Pod");

  await typeAtEnd(page, "deploy.yaml", "metadata:");
  await page.keyboard.press("Enter");
  await page.keyboard.type("  name: web");
  const want = Buffer.concat([bom, Buffer.from("apiVersion: v1\r\nkind: Pod\r\nmetadata:\r\n  name: web")]);
  await expect.poll(() => readFileSync(vault("deploy.yaml")).toString("hex")).toBe(want.toString("hex"));
  await noSeriousA11yViolations(page);
});

test("a shell script stays executable after a save", async ({ page }) => {
  writeFileSync(vault("deploy.sh"), "#!/bin/sh\necho deploy\n");
  chmodSync(vault("deploy.sh"), 0o755);
  await login(page);
  await page.getByRole("button", { name: "deploy.sh" }).click();
  await typeAtEnd(page, "deploy.sh", "echo done");
  await expect.poll(() => readFileSync(vault("deploy.sh"), "utf8")).toBe("#!/bin/sh\necho deploy\necho done");
  expect(statSync(vault("deploy.sh")).mode & 0o777).toBe(0o755);
});

test("a binary file stays listed, and clicking it says why it doesn't open", async ({ page }) => {
  // A PNG's signature and the start of its header, built here: no binary fixture in the repository.
  writeFileSync(vault("logo.png"), Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d]));
  writeFileSync(vault("readme.md"), "# Readme\n");
  await login(page);
  await page.getByRole("button", { name: "readme.md" }).click();
  await page.getByRole("button", { name: "logo.png" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "PostelMD can't open logo.png: it isn't UTF-8 text." })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Text of logo.png" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "logo.png" })).toBeVisible();
});

test("an external edit to a JSON file gives the conflict view, and nothing is lost", async ({ page }) => {
  writeFileSync(vault("config.json"), '{\n  "replicas": 1\n}\n');
  await login(page);
  await page.getByRole("button", { name: "config.json" }).click();
  await expect(page.getByRole("textbox", { name: "Text of config.json" })).toContainText('"replicas": 1');

  writeFileSync(vault("config.json"), '{\n  "replicas": 3\n}\n'); // another writer, outside PostelMD
  await typeAtEnd(page, "config.json", "mine");
  await expect(page.getByRole("heading", { name: "This note changed in another tab or outside this editor" })).toBeVisible();
  await expect(page.getByRole("status", { name: "Save status" })).toHaveText("Not saved: conflict");
  expect(readFileSync(vault("config.json"), "utf8")).toBe('{\n  "replicas": 3\n}\n');
  await expect(page.getByRole("textbox", { name: "Text of config.json" })).toContainText("mine");
});

test("only a Markdown note has a preview", async ({ page }) => {
  writeFileSync(vault("compose.yml"), "services:\n  web:\n    image: nginx\n");
  writeFileSync(vault("plan.md"), "# Plan\n");
  await login(page);
  await page.getByRole("button", { name: "plan.md" }).click();
  await expect(page.getByRole("button", { name: "Preview" })).toBeVisible();
  await page.getByRole("button", { name: "Preview" }).click();
  await expect(page.getByLabel("Preview of plan.md")).toBeVisible();

  await page.getByRole("button", { name: "compose.yml" }).click();
  await expect(page.getByRole("textbox", { name: "Text of compose.yml" })).toContainText("image: nginx");
  await expect(page.getByRole("button", { name: "Preview" })).toHaveCount(0);
  await expect(page.getByLabel("Preview of compose.yml")).toHaveCount(0);
});

test.describe("dark mode", () => {
  test.use({ colorScheme: "dark" });

  test("the cursor has the text's colour, not black", async ({ page }) => {
    writeFileSync(vault("dark.md"), "# Dark\n");
    await login(page);
    await page.getByRole("button", { name: "dark.md" }).click();
    const text = page.getByRole("textbox", { name: "Text of dark.md" });
    const { caret, fg } = await text.evaluate((el) => {
      // --fg as the browser computes it, from a probe that inherits the page's theme.
      const probe = document.createElement("span");
      probe.style.color = "var(--fg)";
      document.body.append(probe);
      const fg = getComputedStyle(probe).color;
      probe.remove();
      return { caret: getComputedStyle(el).caretColor, fg };
    });
    expect(fg).not.toBe("rgb(0, 0, 0)"); // the dark theme is on
    expect(caret).toBe(fg);
  });
});
