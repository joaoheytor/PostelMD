# ADR-0019: UI shell framework: Lit, light DOM, editor in a shadow root

- **Status:** Accepted
- **Date:** 2026-11-09

## Context
The PRD (§7) left the UI shell framework open, to be decided in v0.0 by a spike between
Svelte 5 and Lit. The constraints come from existing decisions:

- `web/src/core` and `@postelmd/plugin-api` stay framework-free (CLAUDE.md rules 3 and 10,
  ADR-0008). State lives in `core` stores; components only subscribe.
- Strict CSP: `script-src 'self'` with no `unsafe-inline` (ADR-0007). Styles must not need
  `unsafe-inline` either (CLAUDE.md rule 6).
- Performance budgets and the measurement procedure in the PRD NFRs.
- Playwright tests use role- and label-based locators only (lesson 14 in
  [lessons-from-frankmd.md](../lessons-from-frankmd.md)).
- No dependency without a reason (CLAUDE.md rule 5); fewer tools in CI is better (ADR-0015).
- Lesson 19: keep the web UI framework-agnostic, so a later rewrite or desktop wrapper doesn't
  mean starting over.

### Spike
The same shell was built three times: a 2,000-note tree (`role="tree"`), a CodeMirror 6
Markdown editor and a status bar (`role="status"`). All three share one framework-free
`Store` and one framework-free editor mount. Versions: Svelte 5.57.3 (with
`@sveltejs/vite-plugin-svelte` 7.3.1), Lit 3.3.3, Vite 8.3.7 and CodeMirror 6 (`view` 6.43.15).
Every build was served with `default-src 'none'; script-src 'self'; style-src 'self';
require-trusted-types-for 'script'` and driven by Playwright in headless Chromium with 4× CPU
throttling, 7 runs each.

| | Vanilla baseline | Svelte 5 | Lit 3 |
|---|---|---|---|
| JS, gzip (CodeMirror is about 168 KB of each) | 168.2 KB | 181.5 KB (**+13.2 KB**) | 174.6 KB (**+6.4 KB**) |
| Mounting the 2,000-item tree, median | 181 ms | 162 ms | 153 ms |
| Glue to subscribe to a `core` store | n/a | 11 lines (`createSubscriber`) | 11 lines (`ReactiveController`) |
| Shell code | 28 lines | 60 lines in 6 files | 58 lines in 2 files |
| Build tooling added | none | compiler + Vite plugin; `svelte-check` for type checking (`tsc` doesn't check `.svelte` files); `eslint-plugin-svelte` for lint | none: plain TypeScript, checked by the existing `tsc` and ESLint |
| Runtime npm dependencies | none | 15 direct dependencies of `svelte` (acorn, magic-string, aria-query…) | 3, all from the Lit project |
| Trusted Types violations | 0 | 0 | 0 |
| Accessibility linting | none | **built into the compiler**; it caught a click handler on an `<li>` with no keyboard handler | none built in (an ESLint plugin exists) |
| Role-based Playwright locators | work | work | work, including into a shadow root |

**CSP finding (independent of the framework).** With `style-src 'self'`, CodeMirror's style
loader (`style-mod`) injects a `<style>` element into the document. CSP blocks it, so the
editor loses its base theme (`.cm-content` gets `white-space: normal`, not `pre`). This
happened in all three builds. When the editor is mounted inside a **shadow root**, `style-mod`
uses constructable stylesheets (`adoptedStyleSheets`) instead. CSP allows those, so there were
0 violations and the theme applied, with no nonce and no `unsafe-inline`.

## Decision
1. The UI shell (`web/src/ui`) uses **Lit 3** custom elements, written in plain TypeScript.
2. Shell components render in the **light DOM** (`createRenderRoot() { return this; }`), so ARIA
   id references (`aria-controls`, `aria-labelledby`, `aria-activedescendant`) work across
   components and one global stylesheet applies.
3. The **editor host mounts CodeMirror inside its own shadow root**. That is the only shadow
   root in the shell, and it is how CodeMirror's styles, and themes added by plugins, comply
   with `style-src 'self'`. We use neither a style nonce nor `unsafe-inline`.
4. The CSP gains `style-src 'self'`, with no `unsafe-inline` and no nonces. A Playwright smoke
   test fails if any CSP or Trusted Types violation is reported (`securitypolicyviolation`)
   while rendering **benign** content. Hostile HTML makes the sanitizer's own parsing report
   violations in recent Chromium, so it is covered by the XSS test in ADR-0007 instead.
5. Components get state only through a `StoreController` (a Lit `ReactiveController` over a
   `core` store). Its subscription is a `Disposable`, released in `hostDisconnected`
   (CLAUDE.md rule 8). `core` and `@postelmd/plugin-api` never import `lit`; ESLint enforces
   that for `core`, and for the plugin API package when it is created.
6. Accessibility: interactive elements are native (`<button>`, `<a>`), or implement the full
   ARIA pattern with keyboard handling. Because Lit has no compiler-level a11y checks, the
   Playwright smoke tests run an automated accessibility scan. The tool is chosen with the
   first UI PR, under this ADR.

## Consequences
- Positive:
  - The smallest addition: +6.4 KB gzip, 3 runtime packages and no build step, compiler or
    second type checker.
  - Custom elements are a web standard, so a later change of framework can replace one
    component at a time (lesson 19).
  - Trusted Types and strict CSP work as-is.
- Negative / costs:
  - No compiler-level accessibility warnings. Svelte flagged a real problem in the spike that
    Lit didn't. The automated a11y scan in the smoke tests replaces that, but it catches
    problems later.
  - Templates are tagged template literals: type checking inside them is weaker than
    `svelte-check`.
  - Fewer contributors know Lit than Svelte.
  - Light DOM means no style encapsulation for shell components. We use component-prefixed
    class names instead.
- Follow-ups:
  - The CodeMirror 6 + Worker spike (roadmap v0.0) builds on the shadow-root editor host.
  - The a11y scan tool is axe-core through `@axe-core/playwright`: the Playwright smoke tests
    fail on any serious or critical violation. It is test tooling and doesn't ship.
  - A tree with 10k notes needs virtualization whichever framework renders it. The spike's
    2,000 items took about 150 ms to mount.

## Alternatives considered
| Option | Why not |
|---|---|
| Svelte 5 | Twice the framework weight (+13.2 KB vs +6.4 KB gzip). It also adds a compiler, a Vite plugin, `svelte-check` and `eslint-plugin-svelte` to CI, and 15 runtime dependencies. Its built-in a11y lint and nicer templates are real advantages, but they don't outweigh the extra toolchain for a small shell whose state already lives in `core`. |
| Vanilla TypeScript (no framework) | Smallest of all, but every component would hand-write DOM diffing and event cleanup: the drift that produced FrankMD's 3,600-line controller (lessons 1 and 9). |
| Lit with shadow DOM for every component | ARIA id references can't cross shadow roots, which breaks tree, tab and listbox patterns. Global styles would need re-adopting in each component. |
| Keep CodeMirror in the light DOM and add a per-response style nonce (`EditorView.cspNonce`) | The server would have to template `index.html` on every request, which stops it being a static, cacheable asset. It also widens `style-src`, which the shadow root avoids. |
| React, Vue, Solid | Not shortlisted. React and Vue are heavier for a small shell; Solid is close to Svelte's trade-offs with a smaller community. None improves on the constraints above. |
