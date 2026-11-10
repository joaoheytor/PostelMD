---
name: milestone-audit
description: Use when the user asks whether a PostelMD roadmap milestone (v0.1, v0.2, …) is done, what its acceptance criteria still lack, or for a test-coverage audit of a milestone. Maps every acceptance criterion in docs/roadmap.md to the automated test that asserts it. Read-only; reports, does not edit.
---

# Milestone audit

Answers "which of this milestone's acceptance criteria does an automated test actually
prove?". **Read-only:** report; don't edit code, tests or docs. The user decides what to do
with the gaps.

## Steps

1. **Criteria.** Read the milestone's section in `docs/roadmap.md` and list every acceptance
   criterion verbatim, numbered. Split a criterion that asserts several things into one line
   per assertion.
2. **Find candidate tests** for each criterion: Go tests (`internal/**/*_test.go`), failure
   scenarios (`test/scenarios/`), Vitest (`web/src/**/*.test.ts`), Playwright (`web/e2e/`),
   script tests (`scripts/*_test.sh`) and the CI-only checks (`.github/workflows/`,
   `Makefile`). For a large milestone, fan the search out to a read-only `Explore` agent per
   area; it returns candidate file:line locations, not verdicts.
3. **Read each test body yourself.** A criterion counts as asserted only if the test drives
   the behaviour and checks the outcome the criterion names. Code existing, a test's name, a
   comment or a passing CI run is not evidence. Check that the test runs in `make ci` (not
   skipped, not behind a build tag CI doesn't set).
4. **Classify** each criterion:
   - **Covered**: a test asserts it; give `file:line` of the assertion.
   - **Partial**: a test exercises it but misses a case the criterion names (another
     browser path, a crash point, an error branch, the image instead of the binary); give
     the evidence and the exact missing case.
   - **Missing**: no test asserts it; say what a test would have to do.

## Report

A table, one row per criterion: criterion, status, evidence (`file:line`), exact gap. Then a
short list of the Missing and Partial rows, most important first (safety and data-loss
criteria before UX). Don't propose fixes beyond naming the test that's needed.
