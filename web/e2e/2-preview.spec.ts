import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

// Runs after 1-workspace.spec.ts, on the vault it set up.
test.describe.configure({ mode: "serial" });

test("the preview renders the note as you type, and stays on after a reload", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "New note" }).click();
  await page.getByLabel("Note name in the vault").fill("preview");
  await page.getByRole("button", { name: "Create" }).click();
  const text = page.getByRole("textbox", { name: "Text of preview.md" });
  await expect(text).toBeVisible();

  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await expect(page.getByRole("button", { name: "Preview", exact: true })).toHaveAttribute("aria-pressed", "true");
  const preview = page.getByRole("region", { name: "Preview of preview.md" });
  await expect(preview.getByRole("heading", { name: "preview", level: 1 })).toBeVisible();

  await text.press("ControlOrMeta+End");
  // The editor continues a task list on Enter, so the second item is typed without its marker.
  await text.pressSequentially("## Steps\n\n| a | b |\n|:--|--:|\n| 1 | 2 |\n\n- [x] page the on-call\nrestart the pod");
  await expect(preview.getByRole("heading", { name: "Steps", level: 2 })).toBeVisible();
  await expect(preview.getByRole("img", { name: "Done", exact: true })).toHaveCount(1);
  await expect(preview.getByRole("img", { name: "To do", exact: true })).toHaveCount(1);
  await expect(preview.getByRole("table")).toBeVisible();
  await expect(page.getByRole("status")).toHaveText("Saved");
  await noSeriousA11yViolations(page);

  await page.reload();
  await expect(page.getByRole("region", { name: "Preview of preview.md" }).getByRole("heading", { name: "Steps" })).toBeVisible();

  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await expect(page.getByRole("region", { name: "Preview of preview.md" })).toHaveCount(0);
});

test("a relative link in the preview opens the note it points to", async ({ page }) => {
  await login(page);
  writeFileSync(join(env("PM_E2E_VAULT"), "links.md"), "# Links\n\nSee [the runbook](ops/runbook.md).\n");
  await page.reload();
  await page.getByRole("button", { name: "links.md" }).click();
  await page.getByRole("button", { name: "Preview", exact: true }).click(); // each test starts with a new browser profile
  await page.getByRole("region", { name: "Preview of links.md" }).getByRole("link", { name: "the runbook" }).click();
  await expect(page.getByRole("textbox", { name: "Text of ops/runbook.md" })).toBeVisible();
});

test("a code block's copy button copies exactly the code", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  writeFileSync(join(env("PM_E2E_VAULT"), "code.md"), "# Code\n\n```sh\nkubectl rollout restart deploy/api\n```\n");
  await login(page);
  await page.getByRole("button", { name: "code.md" }).click();
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  const preview = page.getByRole("region", { name: "Preview of code.md" });
  await preview.getByRole("button", { name: "Copy code" }).click();
  await expect(preview.getByRole("button", { name: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("kubectl rollout restart deploy/api\n");
  await noSeriousA11yViolations(page);
});

test("the editor and the preview scroll together", async ({ page }) => {
  const sections = Array.from({ length: 200 }, (_, i) => `## Section ${i + 1}\n\nStep ${i + 1} of the runbook.\n`);
  writeFileSync(join(env("PM_E2E_VAULT"), "long.md"), `# Long\n\n${sections.join("\n")}`);
  await login(page);
  await page.getByRole("button", { name: "long.md" }).click();
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  const preview = page.getByRole("region", { name: "Preview of long.md" });
  const text = page.getByRole("textbox", { name: "Text of long.md" });
  await expect(preview.getByRole("heading", { name: "Section 200" })).toBeAttached();
  await expect(text).not.toContainText("## Section 200"); // far below: not rendered yet

  // The preview leads: scrolled to the end, the editor follows.
  await preview.hover();
  for (let i = 0; i < 10; i++) await page.mouse.wheel(0, 20_000);
  await expect(text).toContainText("## Section 200");

  // The editor leads: back to the top, the preview follows.
  await text.press("ControlOrMeta+Home");
  await expect(preview.getByRole("heading", { name: "Long", level: 1 })).toBeInViewport();
  await expect(preview.getByRole("heading", { name: "Section 200" })).not.toBeInViewport();
});

test("hostile HTML in a note never runs in the preview", async ({ page }) => {
  test.info().annotations.push({ type: "hostile-html", description: "sanitizer parsing reports CSP violations (ADR-0007)" });
  const hostile = [
    "# Hostile",
    "",
    '<img src="x" onerror="window.__xss = 1">',
    "",
    '<a href="javascript:window.__xss = 2">click</a>',
    "",
    "<script>window.__xss = 3</script>",
    "",
    '<svg><script>window.__xss = 4</script><a xlink:href="javascript:window.__xss = 5">svg</a></svg>',
    "",
    '<iframe srcdoc="<script>parent.__xss = 6</script>"></iframe>',
    "",
    '<form action="https://example.com"><input name="q"><button>go</button></form>',
    "",
    '<p style="position:fixed;inset:0" onclick="window.__xss = 7">styled</p>',
    "",
    "[md link](javascript:window.__xss=8)",
    "",
  ].join("\n");
  writeFileSync(join(env("PM_E2E_VAULT"), "hostile.md"), hostile);

  await login(page);
  await page.getByRole("button", { name: "hostile.md" }).click();
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  const preview = page.getByRole("region", { name: "Preview of hostile.md" });
  await expect(preview.getByRole("heading", { name: "Hostile" })).toBeVisible();
  await expect(preview.getByText("styled")).toBeVisible(); // rendered, so sanitized
  await preview.getByText("styled").click();
  await preview.getByText("click").click();

  const found = await preview.evaluate((el) => ({
    handlers: [...el.querySelectorAll("*")].flatMap((n) => [...n.attributes].filter((a) => a.name.startsWith("on")).map((a) => a.name)),
    jsUrls: [...el.querySelectorAll("[href], [xlink\\:href], [src]")].filter((n) =>
      /^\s*javascript:/i.test(n.getAttribute("href") ?? n.getAttribute("xlink:href") ?? n.getAttribute("src") ?? ""),
    ).length,
    banned: el.querySelectorAll("script, iframe, form, input, button, [style]").length,
  }));
  expect(found).toEqual({ handlers: [], jsUrls: 0, banned: 0 });
  expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBeUndefined();
  expect(readFileSync(join(env("PM_E2E_VAULT"), "hostile.md"), "utf8")).toBe(hostile); // only looked at
});

test("raw HTML across blocks: <details> holds its Markdown, and nothing shows twice after an edit", async ({ page }) => {
  // On disk, not typed: the editor completes and indents HTML tags as they are typed.
  const note = [
    "# HTML blocks",
    "",
    "First para.",
    "<details><summary>Kernel logs</summary>",
    "",
    "Run **dmesg** first.",
    "",
    "</details>",
    "",
    'Forged</div><div class="pm-block" data-line="9999">after',
    "",
    "Middle para.",
    "",
    "<div>",
    "",
    "Unclosed one.",
    "",
    "Last para.",
    "",
  ].join("\n");
  writeFileSync(join(env("PM_E2E_VAULT"), "html-blocks.md"), note);
  await login(page);
  await page.getByRole("button", { name: "html-blocks.md" }).click();
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  const preview = page.getByRole("region", { name: "Preview of html-blocks.md" });
  await expect(preview.getByText("Last para.", { exact: true }).first()).toBeVisible();
  const text = page.getByRole("textbox", { name: "Text of html-blocks.md" });

  // The body is inside the collapsible region: hidden until its summary opens it.
  const details = preview.getByRole("group").filter({ hasText: "Kernel logs" });
  await expect(details.getByText("Run dmesg first.")).toBeHidden();
  await details.getByText("Kernel logs").click();
  await expect(details.getByText("Run dmesg first.")).toBeVisible();

  // Edits re-render only the blocks that changed; no paragraph is duplicated.
  await text.press("ControlOrMeta+Home");
  await page.keyboard.press("End");
  await page.keyboard.type(" again");
  await expect(preview.getByRole("heading", { name: "HTML blocks again" })).toBeVisible();
  await page.keyboard.type(" and again");
  await expect(preview.getByRole("heading", { name: "HTML blocks again and again" })).toBeVisible();
  for (const p of ["First para.", "Run dmesg first.", "Middle para.", "Unclosed one.", "Last para."]) {
    await expect(preview.getByText(p, { exact: true })).toHaveCount(1);
  }

  // The forged wrapper stayed inside a real one: scroll sync sees only the note's own lines.
  const lines = await preview.evaluate((el) => [...el.children].map((c) => c.getAttribute("data-line")));
  expect(lines).not.toContain("9999");
  expect(lines.every((l) => l !== null && Number(l) < 20)).toBe(true);
});
