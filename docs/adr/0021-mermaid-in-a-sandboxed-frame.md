# ADR-0021: Mermaid renders in a sandboxed frame, shown as images

- **Status:** Accepted
- **Date:** 2026-11-10

## Context
Supersedes the **Mermaid** paragraph of [ADR-0007](0007-markdown-and-rendering.md) (main
thread, from the app bundle, its SVG through DOMPurify). The rest of ADR-0007 stands.

The PRD's live preview includes Mermaid diagrams. ADR-0007 also requires
`require-trusted-types-for 'script'` with exactly three policy names, and no inline style.
A spike with Mermaid 12.3.0 inside the app found that this can't hold with Mermaid on the
main thread:

- Even with `securityLevel: 'strict'`, Mermaid assigns plain strings to `innerHTML` while it
  renders. Under Trusted Types every such assignment throws, so no diagram renders.
- Its SVG carries a `<style>` element and `style` attributes, which `style-src 'self'`
  blocks, so even a sanitized SVG would lose its layout and colours.

Making it work in the app's document would mean a `default` Trusted Types policy, which
accepts any string anywhere in the app, plus `style-src 'unsafe-inline'`. CLAUDE.md rule 6
treats both as a weaker CSP that needs an ADR, and they would weaken it for the whole app,
not only for diagrams.

## Decision
- **One renderer frame per open preview**, created only when the note has a `mermaid`
  block, and removed when the preview is hidden or another note opens. It is an `<iframe
  sandbox="allow-scripts">` at `/frames/diagram`, laid out off screen (Mermaid measures text,
  so the document must render), `aria-hidden` and out of the tab order.
- **The server sends the frame its own CSP**, on its page and its script:
  `sandbox allow-scripts; default-src 'none'; script-src 'self'; style-src 'unsafe-inline';
  img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'`. The `sandbox`
  directive gives the frame an opaque origin even when it is opened on its own, so it can't
  read the app's DOM, cookies or storage, or call the API. `default-src 'none'` leaves it no
  network. `X-Frame-Options: SAMEORIGIN` on the page, with both files revalidated
  (`no-cache`). Only these two paths get this policy; every other page keeps the app's CSP,
  which doesn't change.
- **The frame's script is one classic script** with Mermaid inlined (a second Vite build). A
  module script loaded from an opaque origin is a cross-origin request and would need CORS
  headers on our assets.
- **The frame accepts one `MessagePort`, once:** the app sends it on the frame's first `load`.
  The frame takes the first port that comes from its parent, and answers only on it. If the
  frame ever loads again (it navigated), the app drops it and makes a new one, so a page that
  replaced ours never receives a diagram's source.
- **Request and reply:** the app sends the diagram's source and the theme (light or dark). The
  frame renders with `securityLevel: 'strict'` and replies with the SVG, its size from the
  `viewBox`, and a name: the diagram's `accTitle`, else "Diagram: <type>"; or with Mermaid's
  error message. Mermaid's output is HTML and not always well-formed XML, so the frame parses
  it with the HTML parser in an inert `<template>` and serializes it with `XMLSerializer`.
- **The app shows each diagram as `<img src="blob:…">`**, named by its alt text, with an
  intrinsic size when the `viewBox` gives one. An SVG used as an image runs no script and loads
  nothing, and its `<style>` applies inside the image only, so the app's CSP doesn't change
  (`img-src` already allows `blob:`). On an error the code block stays, with the error as text
  under it. Results are cached by source and theme while the preview lives. Disposing the
  preview revokes the blob URLs and removes the frame. A diagram with no reply in 15 s fails,
  and the frame is replaced.
- **Dependency:** `mermaid` 12.3.0, pinned exactly. It is a week old at the time of writing, newer
  than the two weeks we usually wait; the maintainer chose the latest release for this new
  project. It shares `dompurify` with the app (npm dedupes it to one copy).

## Consequences
- Positive: diagrams work with the app's CSP unchanged: still three Trusted Types policy
  names, no inline script or style. A hostile diagram runs, at most, inside an opaque origin
  with no network. Mermaid isn't in the app bundle, so it costs nothing until a note has a
  diagram.
- Negative / costs:
  - The frame's script is about 5.2 MB (1.5 MB gzipped), with every diagram type, KaTeX,
    cytoscape and ELK. Its name has no content hash, so it is revalidated, not cached as
    immutable. A new preview parses it again. Splitting it, or a hashed name the frame page
    learns, is possible later.
  - A diagram is an image: its text can't be selected, and links and `click` callbacks in it
    do nothing (they never run under `strict` anyway). Images inside labels don't show.
  - A hostile diagram can still run script inside the frame, where it could read the sources
    of later diagrams sent to the same frame and leave by navigating the frame. The port dies
    when the frame navigates, and the frame only ever sees diagram sources from the note
    being previewed. Threat model I1.
- Follow-ups: the v0.4 plugin host (ADR-0008) can package this frame as Mermaid's
  first-party plugin; it is already the shape of a sandboxed plugin. Its size if it matters in
  the four-week test (ADR-0020).

## Alternatives considered
| Option | Why not |
|---|---|
| Mermaid on the main thread with a `default` Trusted Types policy that sanitizes, plus `style-src 'unsafe-inline'` | A default policy turns every raw string sink in the app into "sanitize and allow", not only Mermaid's; and inline styles would be allowed app-wide. |
| A fourth named policy for Mermaid | Mermaid doesn't use Trusted Types policies: its sinks get plain strings, so a named policy changes nothing. Inline styles would still be blocked. |
| Show each diagram in its own visible sandboxed iframe | One 5 MB script parse per diagram, and frames that must be sized to their content with messages. The hidden frame and images do one parse per preview. |
| Render in a Worker | Mermaid needs a DOM to measure text. |
| Render on the server | ADR-0011: the server never executes user content, and it would need a headless browser. |
