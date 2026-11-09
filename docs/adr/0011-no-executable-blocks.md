# ADR-0011: No server-side execution of note content

- **Status:** Proposed
- **Date:** 2026-11-09

## Context
"Executable runbooks" (running a code block from the note) are attractive to SRE users.
In a shared, multi-user container that can read every vault, server-side execution is remote
code execution reachable by anyone who can edit a note, or by anything merged from a git
remote.

## Decision
- PostelMD **never executes** note content, templates or code blocks on the server.
- Instead it offers:
  - **copy buttons** on code blocks;
  - **parameterized snippets** (`{{namespace}}`, `{{context}}`), filled in from a small form
    before copying;
  - template placeholders, rendered with logic-less substitution only.
- Execution, if ever offered, will be a **third-party plugin** that hands commands to
  **external** systems the user controls (a local agent, Argo Workflows, a webhook) with
  explicit approval and auditing. It is never part of core and never runs in the PostelMD
  process.

## Consequences
- Positive: removes the highest-impact threat class (threat model E3).
- Negative: less "magic" than some runbook tools; this is documented as a deliberate choice.

## Alternatives considered
| Option | Why not |
|---|---|
| Sandboxed execution in a container | Needs container runtime privileges and a much larger attack surface |
| Client-side execution | Browsers cannot run shell commands; native helpers are a separate product |
