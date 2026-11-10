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

/** Records every Blob the app turns into a URL, so a test can read what an <img> was given. */
async function recordBlobs(page: import("@playwright/test").Page) {
  await page.addInitScript(() => {
    const blobs: Blob[] = [];
    Object.defineProperty(window, "__blobs", { value: blobs });
    const create = URL.createObjectURL.bind(URL);
    URL.createObjectURL = (b: Blob | MediaSource) => {
      if (b instanceof Blob) blobs.push(b);
      return create(b);
    };
  });
  return () => page.evaluate(() => Promise.all((window as unknown as { __blobs: Blob[] }).__blobs.map((b) => b.text())));
}

test("a compromised renderer's SVG is sanitized before it gets the app's origin", async ({ page }) => {
  // The blob URL is the app's origin: opened as a page, a script in it would run as the app.
  // The sanitizer's own parsing reports the handlers it then removes (ADR-0007).
  test.info().annotations.push({ type: "hostile-html", description: "sanitizer parsing reports CSP violations" });
  const hostile =
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 40" onload="alert(1)">' +
    "<script>alert(2)</script>" +
    '<a href="javascript:alert(3)"><text x="4" y="14">kept label</text></a>' +
    '<foreignObject x="0" y="20" width="120" height="20"><div xmlns="http://www.w3.org/1999/xhtml" onclick="alert(4)">' +
    'html label<img src="x" onerror="alert(5)"/><iframe src="about:blank"></iframe></div></foreignObject>' +
    '<set attributeName="onmouseover" to="alert(6)"/><animate attributeName="href" values="javascript:alert(7)"/></svg>';
  await page.route("**/frames/diagram.js", (route) =>
    route.fulfill({
      contentType: "text/javascript",
      body: `addEventListener("message", (e) => {
        const port = e.ports[0];
        if (!port) return;
        port.onmessage = (m) => port.postMessage({ id: m.data.id, ok: true, width: 120, height: 40, label: "Compromised", svg: ${JSON.stringify(hostile)} });
      });`,
    }),
  );
  const blobs = await recordBlobs(page);
  const fence = "```";
  const preview = await openPreview(page, "compromised.md", `${fence}mermaid\nflowchart LR\n  A --> B\n${fence}\n`);
  const img = preview.getByRole("img", { name: "Compromised" });
  await expect(img).toBeVisible();
  expect(await img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0)).toBe(true); // still well-formed XML
  const [svg = ""] = (await blobs()).filter((b) => b.includes("<svg"));
  expect(svg).toContain('viewBox="0 0 120 40"'); // camel-case SVG names survive
  expect(svg).toContain("kept label");
  expect(svg).toContain("html label");
  for (const bad of ["<script", "onload", "onclick", "onerror", "javascript:", "<iframe", "<set", "<animate"]) {
    expect(svg, bad).not.toContain(bad);
  }
});

test("a real diagram keeps its labels through the sanitizer", async ({ page }) => {
  const blobs = await recordBlobs(page);
  const fence = "```";
  const preview = await openPreview(page, "labels.md", `${fence}mermaid\nflowchart LR\n  A[Primary] -->|down| B[Replica]\n${fence}\n`);
  await expect(preview.getByRole("img", { name: "Diagram: flowchart" })).toBeVisible();
  const [svg = ""] = (await blobs()).filter((b) => b.includes("<svg"));
  for (const label of ["Primary", "Replica", "down"]) expect(svg).toContain(label);
});
