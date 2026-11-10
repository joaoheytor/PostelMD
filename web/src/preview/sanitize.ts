import DOMPurify from "dompurify";

/**
 * The only way HTML or a Worker script reaches the page (ADR-0007): the `postelmd-preview`
 * Trusted Types policy. Its createHTML sanitizes with DOMPurify; its createScriptURL accepts
 * only same-origin URLs under /assets/. The CSP allows no other policy names than this one,
 * DOMPurify's own and Lit's (for its template strings).
 */

// Trusted Types aren't in TypeScript's DOM library yet: just what this file uses.
interface Policy {
  createHTML(input: string): unknown;
  createScriptURL(input: string): unknown;
}
declare const trustedTypes:
  | { createPolicy(name: string, rules: { createHTML(s: string): string; createScriptURL(s: string): string }): Policy }
  | undefined;

const config = {
  USE_PROFILES: { html: true },
  // No inline styles (the CSP blocks them) and no forms: a note can't post anywhere.
  FORBID_ATTR: ["style"],
  FORBID_TAGS: ["style", "form", "input", "button", "textarea", "select", "option"],
};

// External links open in a new tab, without the opener or the referrer.
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node instanceof HTMLAnchorElement && /^https?:/i.test(node.getAttribute("href") ?? "")) {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noopener noreferrer");
  }
});

const rules = {
  createHTML: (html: string) => DOMPurify.sanitize(html, config),
  createScriptURL: (input: string) => {
    const url = new URL(input, location.href);
    // The Vite dev server serves the Worker from its source path, not /assets/.
    const ours = url.origin === location.origin && (import.meta.env.DEV || url.pathname.startsWith("/assets/"));
    if (!ours) throw new TypeError(`blocked script URL ${input}`);
    return url.href;
  },
};

// Without Trusted Types (older browsers), the same rules still apply; they return strings.
const policy: Policy = typeof trustedTypes === "undefined" ? rules : trustedTypes.createPolicy("postelmd-preview", rules);

/** Sanitizes untrusted HTML into a fragment, ready to insert. */
export function sanitized(html: string): DocumentFragment {
  const tpl = document.createElement("template");
  tpl.innerHTML = policy.createHTML(html) as string;
  return tpl.content;
}

/** A Worker on one of our own bundled scripts (the URL must be under /assets/). */
export function assetWorker(url: string): Worker {
  return new Worker(policy.createScriptURL(url) as string, { type: "module" });
}
