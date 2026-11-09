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
- **Rendering:** client-side, with markdown-it in a **Web Worker**. Raw HTML is restricted to
  an allow-list, the output goes through **DOMPurify**, and the result is inserted into the DOM.
  - The preview is not rendered while hidden, and rendering is debounced.
  - Source line mapping drives the editor ↔ preview scroll sync.
- **Mermaid:** lazy-loaded only when a `mermaid` block exists; `securityLevel: 'strict'`, and
  its output is sanitized too.
- **Server:** goldmark is used only for extraction. If the server ever returns HTML, it is
  sanitized with bluemonday.
- **Parity:** shared golden fixtures (`testdata/markdown/*.md` with expected extraction output)
  must pass on both parsers.
- **CSP:** `script-src 'self'`, no `unsafe-inline` scripts. `style-src 'self'`, no
  `unsafe-inline` or nonces; CodeMirror's injected styles comply only from a shadow root
  (ADR-0019). `img-src 'self' blob: data:` plus
  admin-approved hosts.

## Consequences
- Positive: no main-thread jank; XSS defense in depth.
- Negative: two parsers, kept in sync with fixtures.

## Alternatives considered
| Option | Why not |
|---|---|
| Server-side rendering of the preview | Round-trip latency on every keystroke; harder offline drafts |
| Same JS parser on the server (would require a TS backend) | Rejected together with the TS backend (ADR-0001) |
