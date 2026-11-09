---
name: new-adr
description: Use when a change needs a new Architecture Decision Record in PostelMD — a new dependency (CLAUDE.md rule 5), a new trust boundary, a change to save/sync/git/backup recovery, a spike result (UI framework, go-git merge), or superseding an Accepted ADR. Also when the user says "write an ADR", "record this decision" or "supersede ADR-00NN".
---

# New ADR

Creates the next ADR from `docs/adr/0000-template.md` and wires it into the docs.
ADRs are public: write for an outside contributor, with no private hostnames, IPs, paths or
work references.

## Steps

1. **Check first.** Read `docs/adr/README.md` and grep `docs/adr/` for the topic. If an ADR
   already covers it:
   - **Proposed** → edit that ADR instead of creating a new one;
   - **Accepted** and only a clarification (dependency rationale, link, typo) → append a dated
     entry under `## Clarifications`; never change its Decision;
   - **Accepted** and the decision changes → continue, as a superseding ADR.
2. **Number:** the highest `docs/adr/NNNN-*.md` + 1, zero-padded to four digits. File name:
   `NNNN-kebab-case-title.md`.
3. **Write it** from the template, `Status: Proposed`, today's date:
   - *Context:* the force behind it, with links to the requirement (PRD), the FrankMD lesson
     (`docs/lessons-from-frankmd.md`) or the issue;
   - *Decision:* plain, testable statements. If the operation has more than one side effect,
     include its **durable intent** and a **recovery table per crash point** (CLAUDE.md rule
     14), and never infer an outcome from a single signal;
   - *Consequences:* positive, negative/costs, follow-ups (with issue links, not TODOs);
   - *Alternatives considered:* at least two real options with the reason each lost.
4. **Superseding:** set the new ADR's context to "Supersedes ADR-XXXX"; in the old ADR change
   only the status line to `Superseded by ADR-NNNN` (allowed even when Accepted).
5. **Wire it in** (same change):
   - add a row to the index table in `docs/adr/README.md`;
   - `docs/threat-model.md`: add or update rows if a trust boundary or data flow changed;
   - `docs/roadmap.md`: add the failure-scenario tests the recovery table needs;
   - `docs/architecture.md`: update the component or flow it changes;
   - `CLAUDE.md`: only if it creates a new hard rule.
6. **Verify:** every relative link resolves, e.g.
   `grep -oE '\]\([^)#]+' docs/adr/NNNN-*.md` and check each target exists; and no other doc
   now contradicts the decision (grep for the old behaviour).
7. Report the new file, the docs touched, and any open question the user must decide.
   Don't commit unless asked.
