import AxeBuilder from "@axe-core/playwright";
import { test as base, expect, type Page } from "@playwright/test";

export { expect };

export const password = "correct horse battery staple";

export function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is unset: run through playwright.config.ts`);
  return v;
}

/**
 * Every test fails on a CSP or Trusted Types violation (ADR-0019). A test that renders
 * hostile HTML opts out with the "hostile-html" annotation: the sanitizer's own parsing
 * reports violations there (ADR-0007), and that test checks what reached the page instead.
 */
export const test = base.extend<{ noPolicyViolations: undefined }>({
  noPolicyViolations: [
    async ({ page }, use, testInfo) => {
      const violations: string[] = [];
      await page.exposeBinding("__pmViolation", (_source, v: string) => void violations.push(v));
      await page.addInitScript(() => {
        const report = (window as unknown as { __pmViolation(v: string): void }).__pmViolation;
        document.addEventListener("securitypolicyviolation", (e) =>
          report(`${e.effectiveDirective} blocked ${e.blockedURI || "inline"} at ${e.sourceFile}:${e.lineNumber}`),
        );
      });
      await use(undefined);
      if (!testInfo.annotations.some((a) => a.type === "hostile-html")) expect(violations).toEqual([]);
    },
    { auto: true },
  ],
});

export async function noSeriousA11yViolations(page: Page): Promise<void> {
  const { violations } = await new AxeBuilder({ page }).analyze();
  expect(violations.filter((v) => v.impact === "serious" || v.impact === "critical")).toEqual([]);
}

export async function login(page: Page): Promise<void> {
  await page.goto("/");
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByRole("navigation", { name: "Notes" })).toBeVisible();
}
