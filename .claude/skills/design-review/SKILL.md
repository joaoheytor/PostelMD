---
name: design-review
description: Use when the user asks for a design review, architecture review or consistency check of PostelMD's docs (PRD, architecture, ADRs, threat model, roadmap, ci-cd), before implementing a recovery path, or after a batch of ADR edits. Finds contract gaps, missing crash points, contradictions between documents and unsafe claims. Read-only; reports findings, does not edit.
---

# Design review

Reviews the design documents the way the external reviews of this project did: contract by
contract, looking for states the spec doesn't handle. **Read-only:** report findings; the
user decides what to apply.

## Scope

Default: the docs changed since the last commit (`git diff --name-only HEAD -- docs CLAUDE.md`),
plus every doc they reference. If nothing changed, or the user asks for a full review, cover
all of `docs/`. Always read `CLAUDE.md` first: its hard rules are the review baseline.

## Run it

Dispatch one `general-purpose` subagent per lens below (in parallel; read-only, Read/Grep/Glob
only). Give each the file list and its lens, and ask for findings with **file:line**, the
**concrete failing scenario**, and the **fix the spec needs**. Then verify every finding
yourself against the files before reporting it; drop anything you can't confirm.

1. **Crash and recovery contracts.** For every operation with more than one side effect: is
   there a durable intent before the first side effect, a recovery table for *that* operation
   and mechanism, and an outcome for every crash point? Does recovery ever decide from a
   single signal (HEAD, a hash, a timeout)? Can a "natural" crash state be misclassified as
   interference, or the reverse?
2. **Concurrency and identity.** Races between tabs, users, the reconciler, git, backups and
   external writers; identity reuse (IDs, generations, `esid`); fallbacks that rely on a
   timeout for exclusivity.
3. **Security and trust boundaries.** Authorization on every request and event; cached
   decisions standing in for authorization; restore or upgrade reactivating revoked access;
   plugin isolation (navigation, channels, capabilities); secret scanning covering every
   byte and metadata field that leaves; secure defaults (CLAUDE.md rule 6).
4. **Cross-document consistency.** The same fact stated differently in PRD, architecture,
   ADRs, threat model, roadmap, ci-cd and CLAUDE.md (limits, defaults, policies, names,
   milestones); claims that a newer ADR has narrowed; roadmap scenarios missing for a
   recovery table; threat-model rows missing for a boundary.
5. **Release and supply chain.** Workflow triggers, tag classes, signatures and attestation
   at every registry address, required-check semantics, permissions, pinning.

## Report

- Group as **High** (data loss, privilege, unsafe claim) / **Medium** (race or gap with a
  workaround) / **Low** (consistency, wording).
- Each finding: title, file:line, failing scenario, needed fix. Keep it short.
- Then a table of items you checked that are **resolved**, so the user sees coverage.
- End with your judgement: what must be fixed before implementing, and what can be proven
  instead by the failure-scenario suite (`test/scenarios/`).
