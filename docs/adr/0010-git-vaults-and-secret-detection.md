# ADR-0010: Git-backed vaults and secret detection in core

- **Status:** Proposed (in v0.1)
- **Date:** 2026-11-09 (revised after design review)

## Context
The target audience works docs-as-code: runbooks and ADRs live in git and are published by
MkDocs or TechDocs. Operational notes are where API keys and kubeconfigs get pasted by
accident. **Removing a secret from the current file does not remove it from git history**, so
detection must act *before* a commit, and every outgoing push must be scanned in full. Git
state, concurrency and recovery are specified in ADR-0016.

## Decision

### Git-backed vaults
- Every vault is a git working tree, managed with **go-git**, since the distroless image has no
  `git` binary.
- **Auto-commit** after a quiet window (default 30 s, configurable per vault), plus an optional
  manual commit with a message. Commit and author rules are in ADR-0016.
- Push and pull to an optional remote.
- **Remote credentials are vault-owned.**
  - A vault owner configures them, and the owner who configured them is recorded as the
    *credential steward*.
  - They are encrypted with the `credentials` key (ADR-0005).
  - They are **suspended automatically** (push and pull stop, the vault shows "remote
    credentials need re-confirmation") when the steward is suspended or deleted, loses the
    owner role, or leaves the vault. Another owner must re-confirm or replace them.
  - Background jobs never use a suspended credential.
  - **Least privilege:** a steward token needs only repository contents, read and write. A
    GitHub classic token whose `X-OAuth-Scopes` include `workflow` is refused on the first
    authenticated request; for other providers and fine-grained tokens, the setup screen and
    docs say which scopes to leave out. Owner-only control files (ADR-0006) cover the editor
    side.
  - Remote URLs, schemes and SSH host keys follow the egress policy (architecture §11).
- History and diff per file; "restore this version" creates a new commit.

### Secret detection: four gates
1. **Paste (editor):** a CodeMirror extension runs the same rule set compiled to JS-compatible
   regexes and shows an inline warning. Advisory only.
2. **Save (server, synchronous):** the gitleaks rule set (Go library) scans the saved content.
   Findings are returned in the save response (ADR-0003 §5) and stored.
3. **Commit gate (scan exactly what is committed):** stored findings are *not* trusted at
   commit time, because an external writer may have changed the file since the last scan.
   For each candidate file, the committer:
   1. reads the file **once** into memory;
   2. computes its git blob hash;
   3. scans **those bytes**;
   4. if they are clean, writes **that same byte slice** as the blob object and stages that
      blob ID;
   5. builds the commit only from blobs that passed this check;
   6. **scans the exact commit metadata it is about to write**: message (auto-generated or
      typed by the user), author and committer name and email, and `Co-authored-by` trailers.
      A detection rejects the commit with an inline error on the message field. It is never
      written to the object database.

   A file whose bytes have an unresolved finding is not staged; it stays modified and the UI
   shows "not committed: secret detected".

   **The cache stores detections, never decisions.**
   - Raw detections from **content-only rules** are cached by `(blob hash, ruleset version)`.
     A cache entry is a list of detections (rule, position, fingerprint), not "clean" or
     "allowed".
   - **Path-dependent rules** and path-scoped allow-lists are evaluated at every gate for the
     actual `(vault, path, blob)`, together with the vault's current approvals. Gitleaks also
     supports path-scoped rules and allow-lists.
   - So identical bytes approved under `fixtures/**` are **not** approved when copied
     elsewhere.
4. **Push gate (everything the destination would receive):**
   - The scan is bound to **(remote URL, ref, exact commit SHA being pushed)**.
   - Before a push, enumerate **every blob reachable from that commit** and scan each blob
     whose content-only detections aren't cached yet for the current ruleset version. Then
     re-evaluate path rules and approvals for every reachable `(path, blob)` pair.
   - **Metadata is scanned too.** Every outgoing commit's message, author and committer
     fields, and every annotated tag message being pushed, are scanned (cached by object hash).
     A token in a commit message blocks the push like one in a file.
   - **Names are scanned too:** every path in the reachable trees and every ref name being
     pushed (a file called `AKIA….txt` or a branch named after a token leaks it as surely as
     content does).
   - This needs **no baseline**: it works the same for a first push, an empty remote, a new
     branch or a changed remote. Thanks to the cache, steady-state pushes only scan new blobs.
   - Changing the ruleset version invalidates the cache, so the next push rescans everything.
   - If the reachable set can't be enumerated (a corrupt object, a missing pack), the push
     **fails closed**.
   - This covers commits created outside PostelMD too.

   The push is blocked if any reachable blob has an unresolved finding. If the secret only
   exists in history, the UI says so and explains the options:
   - rotate the secret, then acknowledge it; or
   - rewrite local, unpushed history (a guided "drop/squash unpushed commits"; never for
     commits already on the remote).

### Findings
- **Identity:** a *fingerprint*, `HMAC-SHA256(fingerprint key, ruleID ‖ secretValue)`, using
  the instance's `fingerprint` key (ADR-0005). A plain hash would let anyone holding a
  fingerprint test guesses offline against short or structured secrets. The secret value
  itself is never stored or logged; only the fingerprint, rule, file ID, line and commit are
  stored. The same secret in several places is one finding with several locations.
  - Fingerprints never leave the instance: exports (the allow-list file, findings exports)
    carry only the rule and path.
  - The `fingerprint` key is rotated only after a compromise. Rotation re-fingerprints by
    rescanning, and owners re-approve acknowledgements that can't be carried over by
    `(rule, location)`.
- **States:** `open` → `resolved` (no longer present in the working tree or the outgoing range)
  | `acknowledged` (a false positive, or the credential has been rotated).
- **Authority:**
  - a vault **owner** may acknowledge a finding, with a required reason
    (`false_positive` | `rotated` | `test_data`);
  - editors may request acknowledgement but not grant it;
  - instance admins may acknowledge in any vault;
  - acknowledgements are audit-logged (who, when, reason, fingerprint).
- **Allow-list authority lives in the database, not in the repository.**
  - Approved allow-list entries are stored in SQLite, with the approving owner, the reason and
    the time. An entry is a **fingerprint**, optionally limited to a path glob. A path glob
    **never suppresses a fingerprint nobody approved**: a new secret under `fixtures/**` is
    an open finding until an owner approves it.
  - `.postelmd-secrets-allow` in the repository is only an **export** (written through the
    findings API), listing rule and path per entry, never fingerprints. Another instance
    shows them to its owners as pending entries to review.
  - Any change to that file that arrives through a **pull or an external edit is inert**: the
    scanner ignores it, and the UI lists it as "pending allow-list changes" until a vault owner
    approves each entry.
  - Repository write access therefore never grants authority to acknowledge secrets.
  - Rule-level disables are admin-only, instance-wide.

## Consequences
- Positive: secrets don't enter history through PostelMD. **Everything a PostelMD push
  sends is scanned**: blobs, commit messages, author and committer fields, and annotated tag
  messages. Git notes and other refs aren't pushed by PostelMD. History and backup come for free. It is a clear differentiator.
- Negative:
  - v0.1 gets bigger;
  - the first push scans the whole reachable history once; later pushes only scan new blobs;
  - files with open findings stay uncommitted until handled, which is by design;
  - go-git has gaps compared with the git CLI (ADR-0016 limits the supported states).

## Alternatives considered
| Option | Why not |
|---|---|
| Scan only the working tree before push | Misses secrets in earlier commits |
| Auto-commit everything, rewrite history later | Rewriting history is risky and often too late |
| Shell out to `git` | Requires a shell and git in the image; larger attack surface |
| Git as a plugin | It is core to data safety and positioning |
| Secret detection only via CI pre-commit hooks | Too late: the secret already sits on the server |
