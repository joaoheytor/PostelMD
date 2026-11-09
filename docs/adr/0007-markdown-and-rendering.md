# ADR-0007: Markdown dialect and client-side rendering

- **Status:** Proposed
- **Date:** 2026-11-09

## Context
The preview must be instant and must never execute untrusted script. The server needs to
parse Markdown only for extraction: headings, links, tags and backlinks for the index.

## Decision
- **Dialect:**
  - CommonMark + GFM (tables, task lists, strikethrough, autolinks);
  - YAML frontmatter;
  - fenced code blocks with `mermaid` support;
  - `[[wikilinks]]` added post-MVP.
- **Rendering:** client-side. markdown-it runs in a **Web Worker** and returns the note as a
  list of top-level blocks (HTML plus source line). Raw HTML is restricted to an allow-list.
  - **Worker output is untrusted.** DOMPurify needs a DOM, which Workers lack, so it runs on
    the main thread, inside the Trusted Types policy (below).
  - **Only changed blocks are sanitized and replaced.** The main thread keeps each block's
    source HTML and patches only the blocks whose HTML changed. Re-sanitizing the whole note
    on every edit is forbidden (about 1 s per edit on a 10k-line note in the spike).
  - **Initial render is batched and yields:** about 200 blocks per sanitize call, yielding to
    the event loop between batches (a `MessageChannel`, not `setTimeout`, which is clamped).
  - Preview blocks use `content-visibility: auto`, so off-screen blocks cost nothing while
    typing. Without it, a large preview slowed every keystroke.
  - The preview is not rendered while hidden, and rendering is debounced.
  - Source line mapping (`data-line` on each block) drives the editor ↔ preview scroll sync.
- **Trusted Types:** `require-trusted-types-for 'script'` with exactly two policy names:
  - `postelmd-preview` (ours): its `createHTML` sanitizes with DOMPurify, and its
    `createScriptURL` accepts only same-origin URLs under `/assets/` (the Worker is a script
    URL too). It is the only way HTML or a Worker reaches the page.
  - `dompurify`: DOMPurify's own internal policy for parsing into an inert document.
- **Mermaid:** lazy-loaded only when a `mermaid` block exists; `securityLevel: 'strict'`, and
  its output is sanitized too.
- **Server:** goldmark is used only for extraction. If the server ever returns HTML, it is
  sanitized with bluemonday.
- **Parity:** shared golden fixtures (`testdata/markdown/*.md` with expected extraction output)
  must pass on both parsers.
- **CSP:** `script-src 'self'`, no `unsafe-inline` scripts. `style-src 'self'`, no
  `unsafe-inline` or nonces; CodeMirror's injected styles comply only from a shadow root
  (ADR-0019). `worker-src 'self'`. `trusted-types postelmd-preview dompurify`.
  `img-src 'self' blob: data:` plus admin-approved hosts.

### Spike results (2026-11-09: CodeMirror 6 + markdown-it 15.0.3 + DOMPurify 3.4.17)
A 10,000-line synthetic note (headings, lists, tables, code, quotes, hostile raw HTML) was
edited by Playwright in headless Chromium at 4× CPU throttling: 150 keystrokes at line 5,000,
3 runs per mode, under the CSP and Trusted Types above, with 0 violations.

| Mode | Keystroke-to-paint p95 | Keydown JS processing p95 | Per edit (main thread) | Initial render (main thread) |
|---|---|---|---|---|
| Plain `<textarea>` (control) | 64 ms | 0.3 ms | n/a | n/a |
| CodeMirror, preview hidden | 48 ms | 1.0 ms | n/a | n/a |
| Full re-render | 104 ms | n/a | ~1,000 ms (sanitize) | ~1,050 ms in one task |
| Changed blocks only, no `content-visibility` | 120 ms | 1.1 ms | 9 ms | 4.4 s (3,334 sanitize calls) |
| **Decision above** (changed blocks + batched initial render + `content-visibility`) | **48 ms** | **1.0 ms** | **7 ms** | 1.7 s in slices ≤ 65 ms |

- The Worker parses and renders the whole note in about 60 ms.
- **Measurement finding:** in headless CI, even a plain `<textarea>` can't paint within 16 ms
  (software rendering at 4× throttling), so an absolute keystroke-to-paint budget measures
  the runner, not PostelMD. The PRD budget is now relative to the `<textarea>` control, and
  it also caps our own keydown processing time.
- Build: Vite only bundles the Worker when it is imported as `./worker.ts?worker&url`.
  Wrapping `new URL(…, import.meta.url)` in the policy call made Vite inline the TypeScript
  source as a `data:` URL instead.

## Consequences
- Positive: typing in a 10k-line note costs the same with the preview open as with it hidden;
  XSS defense in depth (sanitizer inside a Trusted Types policy, strict CSP).
- Negative: two parsers, kept in sync with fixtures. The main thread still sanitizes (DOMPurify
  can't run in a Worker); the block patching and batching above are what keep that cost off
  the typing path.

## Alternatives considered
| Option | Why not |
|---|---|
| Server-side rendering of the preview | Round-trip latency on every keystroke; harder offline drafts |
| Sanitizing in the Worker | DOMPurify needs a DOM; Workers have none. A DOM-free sanitizer would be new, less-proven security code |
| Re-render and re-sanitize the whole preview on each edit | About 1 s of main-thread work per edit on a 10k-line note |
| Same JS parser on the server (would require a TS backend) | Rejected together with the TS backend (ADR-0001) |
