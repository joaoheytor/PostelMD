# Lessons from FrankMD

PostelMD was inspired by [FrankMD](https://github.com/akitaonrails/FrankMD), a self-hosted
Markdown editor that Fabio Akita built with Claude Code and described in two blog posts:
[part 1](https://akitaonrails.com/2026/02/01/vibe-code-fiz-um-editor-de-markdown-do-zero-com-claude-code-frankmd-part-1/)
and [part 2](https://akitaonrails.com/2026/02/01/vibe-code-fiz-um-editor-de-markdown-do-zero-com-claude-code-frankmd-parte-2/).
This page records the problems reported in those posts and seen in the repository's issues
and commits (analysis as of 2026-11), and how PostelMD avoids each one. **No FrankMD code is
used.**

| # | What happened in FrankMD | PostelMD's answer |
|---|---|---|
| 1 | No CLAUDE.md or conventions on day 1. Non-RESTful routes and logic dumped into a few services, later a ~1k-line refactor | `CLAUDE.md` with layering and REST rules before any code; ADRs |
| 2 | All JS in one 4–5k-line file; `app_controller.js` is still ~3.6k lines with ~215 methods | 500-line hard limit in CI; framework-free `core/` stores; no DOM-event coordination |
| 3 | Custom textarea overlay for syntax highlighting caused alignment and scroll bugs; replacing it with CodeMirror took 6k+ changed lines | CodeMirror 6 from the first commit |
| 4 | Autosave on every keystroke; word count on every change; preview rendered while hidden | Performance budgets in the PRD; debounce; worker rendering; skip when hidden |
| 5 | Autosave recreated renamed or deleted files; stale tree entries; races (#170–#185) | Save protocol designed up front (ADR-0003); updates never create |
| 6 | The revision check did nothing for months (response body never parsed) | Contract tests on the save API; metrics on conflicts; e2e conflict test |
| 7 | External edits only noticed on the next save; no file watching (#272) | Watcher + reconciler + SSE (ADR-0004) |
| 8 | Linear regex scan for search and backlinks on every request | SQLite FTS5 index, rebuildable |
| 9 | Auth off by default; binds to `0.0.0.0`; CSRF origin check disabled; secret regenerated at each start; `/logs/tail` exposed; CSP `img-src *` | Secure by default (ADR-0005, ADR-0009); persisted key; no log endpoint |
| 10 | XSS sanitizer and SSRF protection added late, by contributors | Threat model + sanitizer + egress policy from v0.1 |
| 11 | `.fed` mixed secrets, preferences and history, with surprising precedence rules | Secrets in env/`_FILE`; preferences in the DB; one precedence rule |
| 12 | Integration sprawl (web image scraping, AI images, ~20 themes, 7 locales) | Non-Goals list; plugins for extras |
| 13 | Hand-vendored importmap JS dependencies | npm + lockfile + Vite; Renovate |
| 14 | DOM-coupled system tests broke on every UI change | Playwright with role-based locators; few, stable smoke tests |
| 15 | Global AI client configuration raced between concurrent requests | No process-global mutable clients; per-request config |
| 16 | `.gitignore` entry `notes/` silently broke the Tailwind build (~2 h lost) | Anchored ignore patterns (CLAUDE.md rule) |
| 17 | `setTimeout` without `clearTimeout`; dead code left after refactors | Disposable rule; dead code removed in the same PR |
| 18 | Unsupervised agent runs, vague bug reports, a 1-day estimate that took 3 days, burnout | Human review of every diff; bug-report template; one epic at a time |
| 19 | The author regretted choosing Rails (no desktop path); a later native Rust rewrite | Go single binary; framework-agnostic web UI; desktop-wrapper path kept open |
