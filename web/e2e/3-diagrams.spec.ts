import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { env, expect, login, noSeriousA11yViolations, test } from "./helpers";

async function openPreview(page: import("@playwright/test").Page, name: string, text: string) {
  writeFileSync(join(env("PM_E2E_VAULT"), name), text);
  await login(page);
  await page.getByRole("button", { name }).click();
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  return page.getByRole("region", { name: `Preview of ${name}` });
}

test("a mermaid block is shown as a diagram, named by its title", async ({ page }) => {
  const fence = "```";
  const preview = await openPreview(
    page,
    "diagram.md",
    `# Failover\n\n${fence}mermaid\nflowchart LR\n  accTitle: Failover path\n  A[Primary] -->|down| B[Replica]\n${fence}\n\n${fence}mermaid\nsequenceDiagram\n  Alice->>Bob: page\n${fence}\n\n${fence}mermaid\nnot a diagram\n${fence}\n`,
  );
  const named = preview.getByRole("img", { name: "Failover path" });
  await expect(named).toBeVisible();
  expect(await named.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
  await expect(preview.getByRole("img", { name: "Diagram: sequence" })).toBeVisible();
  await expect(preview.getByText("This diagram has an error:")).toBeVisible();
  await expect(preview.getByText("not a diagram", { exact: true })).toBeVisible(); // the code stays
  await noSeriousA11yViolations(page);

  // An edit to the diagram re-renders it.
  await page.getByRole("textbox", { name: "Text of diagram.md" }).click();
  await page.keyboard.press("ControlOrMeta+End");
  // No indentation: CodeMirror keeps the previous line's, and an indented fence doesn't close.
  await page.keyboard.type('\n```mermaid\npie\naccTitle: Error budget\n"spent" : 30\n"left" : 70\n```\n');
  await expect(preview.getByRole("img", { name: "Error budget" })).toBeVisible();
});

test("a hostile diagram stays inside the sandboxed renderer", async ({ page }) => {
  // The frame's own CSP blocks the hostile label's handler and image, and reports doing so.
  test.info().annotations.push({ type: "hostile-html" });
  await page.addInitScript(() => {
    Object.defineProperty(window, "__xss", { value: 0, writable: true });
    // Each document keeps its own list: the app's must stay empty, whatever the frame reports.
    const seen: string[] = [];
    Object.defineProperty(window, "__violations", { value: seen });
    document.addEventListener("securitypolicyviolation", (e) => seen.push(`${e.effectiveDirective} ${e.blockedURI}`));
  });
  const fence = "```";
  const preview = await openPreview(
    page,
    "hostile-diagram.md",
    [
      `${fence}mermaid`,
      "flowchart LR",
      '  A["<img src=x onerror=parent.__xss=1>"] --> B[<script>parent.__xss=2</script>]',
      '  click A "javascript:parent.__xss=3"',
      "  click B call alert()",
      fence,
      "",
    ].join("\n"),
  );
  await expect(preview.getByRole("img", { name: "Diagram: flowchart" })).toBeVisible();
  const frame = page.locator("iframe.pm-diagram-frame");
  await expect(frame).toHaveAttribute("sandbox", "allow-scripts");
  // The frame's origin is opaque: the app can't read its document, so it can't read the app's.
  expect(await frame.evaluate((f: HTMLIFrameElement) => f.contentDocument)).toBeNull();
  await preview.getByRole("img", { name: "Diagram: flowchart" }).click();
  expect(await page.evaluate(() => (window as unknown as { __xss: number }).__xss)).toBe(0);
  await expect(preview.locator("script, iframe, [onerror], [onclick]")).toHaveCount(0);
  expect(await page.evaluate(() => (window as unknown as { __violations: string[] }).__violations)).toEqual([]);
});
