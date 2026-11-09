# PostelMD: Threat Model (STRIDE)

- **Status:** Draft v0.2 (revised after design review). Update this file whenever a trust
  boundary changes.
- **Last updated:** 2026-11-09

**MVP (v0.1, [ADR-0020](adr/0020-mvp-scope.md)):** one user and no remote, so the
multi-user rows (horizontal and vertical escalation, roles, invites, membership changes),
the remote and push rows, and the plugin rows don't apply yet. The MVP adds:
- **a stolen session cookie** is valid until it expires (7 days) or the password changes;
  one session can't be revoked server-side. Mitigations: `HttpOnly`, `SameSite=Lax`,
  `Secure` on TLS, "log out everywhere" bumps the epoch, and a password change needs the
  current password, so a stolen cookie can't lock the owner out;
- **`auth.json` and `secret.key`** in `/data/.postelmd/` (`0600`, reserved paths) are the
  whole credential: anyone who can read the volume can forge sessions, as with the key ring
  in the full design.

## Assets
1. Note contents (possibly sensitive operational knowledge)
2. Credentials: user passwords and passkeys, sessions, git remote tokens, the OIDC client
   secret, the key ring
3. Secrets accidentally pasted into notes, including in git history
4. Integrity and attribution of the git history
5. Availability and consistency of the instance (single writer, backups)

## Actors
- **Anonymous network attacker** (LAN, Tailscale peer, or the internet if exposed)
- **Authenticated user** trying to reach other users' vaults (horizontal escalation) or admin
  functions (vertical escalation)
- **Former user or member** whose access was revoked
- **Malicious or compromised plugin**
- **Malicious note content** (shared vault, `git pull` from a remote)
- **External writer** (a well-meaning tool that races with PostelMD)
- **Operator error** (force-deleting a pod, two containers on one volume)
- **Supply-chain attacker** (dependencies, build pipeline, fork PRs)

## Trust boundaries
```
[Browser main window] ──TLS?── [postelmd HTTP] ── [service/authz] ── [store/*: os.Root + reserved paths] ── [/data]
   │                                   │                    │
[per-plugin opaque iframe (+ its workers)]   [egress client] → git remotes (https only; I13), IdP, registry, approved hosts
```

## STRIDE

| # | Threat | Mitigation | ADR |
|---|---|---|---|
| S1 | Forged identity header | Trusted-header auth off by default. When enabled: TCP peer in `TRUSTED_PROXIES` only, header rejected from untrusted peers, NetworkPolicy, explicit identity linking | 0005 |
| S2 | Credential stuffing or brute force | argon2id, per-account backoff and per-IP limits (never a lockout an attacker could trigger), passkeys, invite-only signup | 0005 |
| S5 | Credential change from a hijacked session | Re-authentication within 5 min for every credential change; removing a credential revokes the other sessions; passkeys reviewed during a reset | 0018 |
| S3 | First-run setup hijack | Single-use, expiring token printed to logs only; headless `ADMIN_*_FILE` alternative | 0009 |
| S4 | Account takeover via an OIDC email match | No automatic linking by email; linking requires a fresh login | 0018 |
| T1 | Path traversal or symlink escape | `os.Root` per vault; symlinks not followed; fuzz tests | 0006 |
| T2 | Access to `.git/`, `.postelmd/` or the allow-list through the API, or reserved paths arriving in a pulled commit | `store.IsReserved` on every operation, including watcher, commit-scan, staging and pull; 404; a pull with a reserved path is refused; test matrix | 0006, 0016 |
| T3 | Lost update between PostelMD clients | ID + generation + hash ETag; locks; atomic rename | 0003 |
| T4 | ABA: an old autosave applied to a recreated file | File IDs + tombstones → 410/412 | 0003 |
| T5 | Overwrite by a race with an external writer | Re-`stat` before rename; detection metric; maintenance mode. The lost bytes are neither detectable nor recoverable (ADR-0003 §1). **Residual, documented.** | 0003 |
| T6 | A move clobbers a destination | No-replace rename; 409 | 0003 |
| T7 | Two writers on one volume (split brain) | Instance `flock`; boot-UUID check; RWOP PVC; fencing runbook; network-filesystem refusal | 0014 |
| T9 | A secret slips in between the scan and the commit (external writer) | Commit gate scans and stages the **same in-memory bytes**; results bound to the blob hash | 0010 |
| T10 | Repository write access acknowledges secrets (imported allow-list) | Allow-list authority lives in the DB; imported changes are inert until an owner approves them | 0010 |
| T11 | A crash mid-merge is discarded as "didn't happen" | Intent evidence covers the index, working tree and merge state; never discard; recovery copies | 0016 |
| T12 | Rollback resurrects revoked access | DB-only rollback only before any mutation; otherwise full restore + access revalidation | 0015 |
| T8 | Tampered image, chart or binary | cosign with pinned identities; SBOM; digest pinning | 0015 |
| T8b | A forged or misplaced release tag starts a release | Tag ruleset (admin only); `scripts/verify-release-tag.sh` before any release job: SemVer name, annotated tag, SSH signature from a key in `.github/allowed_signers` on `main` (revocable), tagged commit on `main` | 0015 |
| R1 | Who changed or deleted a note, or who acknowledged a secret? | Per-author git commits; external changes labelled; append-only audit log (365 days) | 0016, arch §9 |
| R2 | Audit records edited or removed after the fact | External sink; the `file:` sink is HMAC-chained with a key that stays in `/data`; chain head also logged to `stdout`; `postelmd audit verify` | arch §9 |
| I1 | Stored XSS through Markdown | markdown-it (restricted HTML) → DOMPurify inside the only Trusted Types policy; CSP `script-src 'self'`, `style-src 'self'` and `require-trusted-types-for 'script'`; a smoke test fails on any CSP violation; Mermaid in strict mode | 0007, 0019 |
| I2 | A plugin exfiltrates notes or reads the session | Sandboxed plugins in a per-plugin opaque iframe, with Workers created inside it and a per-plugin CSP; capability RPC; negative tests. The trusted tier is admin-only. | 0008 |
| I3 | A secret reaches git or a remote | Paste warning; synchronous save scan; commit gate; scan of the **whole outgoing range**, including paths and ref names, before push; owner-only acknowledgement; path globs never suppress unapproved fingerprints; HMAC fingerprints never exported | 0010 |
| I12 | `/metrics` reveals operational detail | Token on the main listener (`404` without one) or a separate scrape-only listener | 0009 |
| I4 | SSRF through server-side fetches or git remotes | Egress client: only globally reachable unicast after `Unmap()` (special-purpose, NAT64 and 6to4 blocks denied), exact-host exceptions per purpose, pinned DNS, re-checked redirects; git remotes `https`/`ssh` only, SSH through the same dialer with pinned host keys | arch §11, 0010 |
| I13 | A vault reads another vault, or the host, through its git remote (a local path, `file://`, or an `insteadOf` rewrite in an imported `.git/config`) | The remote URL is a trust boundary: `https://` only, no credentials, query or fragment, checked by two parsers before it is stored and again before every fetch or push (after `insteadOf` rewrites); a stored remote that fails puts the vault in `unsupported`. At startup go-git keeps only the `https` transport, through the egress client, so a URL that skips the check still can't reach the filesystem (#40) | 0016 |
| I5 | Uploaded SVG or HTML executes | Content sniffing + allow-list; `nosniff`; SVG served as an attachment | 0009 |
| I6 | Logs leak content or secrets | Never log bodies, secret values or tokens; no log endpoint | 0009 |
| I7 | A revoked user keeps receiving events | Per-event authorization; stream closed on revocation; replay re-authorized | 0018 |
| I8 | Drafts left on a shared browser | Namespaced drafts; logout prompt (default: delete); "shared computer" mode | 0018 |
| I9 | Backup theft | age encryption to an off-instance recipient; plaintext only with an explicit flag | 0017 |
| I10 | Key compromise | Purpose-separated, versioned keys; rotation; compromise runbook | 0005 |
| D1 | Huge files or requests stall the server | Size limits; timeouts; FTS instead of a regex scan | 0003, 0009 |
| D2 | Watcher event storm | Coalescing; fall back to reconcile; bounded outbox | 0004 |
| D3 | Slow SSE consumers exhaust memory | Per-connection buffer limits → `resync` | arch §5 |
| D4 | Full disk corrupts state | `ENOSPC` → read-only vault, `507`, drafts kept, alert | 0003 |
| E1 | A viewer writes, or an editor manages members | Service-layer authorization on every request; ≥ 1 owner invariant; table tests | 0006, 0018 |
| E2 | A fork PR runs code on the homelab runners | Public CI on hosted runners only | 0015 |
| E3 | Code execution through runbook blocks | Not supported, by design | 0011 |
| I11 | A sandboxed plugin exfiltrates through iframe self-navigation | Headless plugins (code in a Worker, no document access); a DOM plugin plus data-read needs admin approval with an explicit warning; navigation detection disables the plugin | 0008 |
| E7 | A demoted OIDC admin keeps privileges | `admin_oidc` is authoritative and recomputed; stale-privilege window ≤ the role TTL (1 h) | 0005 |
| E8 | A restore reactivates a suspended account (including an admin), a revoked invite or reset link, or a withdrawn plugin approval | Quarantine trusts no restored identity; an operator-created recovery admin approves accounts individually (admins always individually); pending invites, reset links and setup tokens are purged; plugin safe mode; the recovery identity is disabled afterwards | 0017 |
| E9 | A vault editor changes a CI workflow that runs with the steward's token | Owner-only control files; external changes to them wait for an owner; steward tokens without workflow scope | 0006, 0010 |
| E5 | A plugin spoofs another plugin's identity over RPC | Identity bound to a dedicated MessageChannel port, invalidated on navigation | 0008 |
| E6 | A suspended user's git token keeps being used | Vault-owned credentials with a steward; suspended automatically when the steward loses access | 0010 |
| E4 | A plugin update silently gains capabilities | Approval bound to the capabilities hash; re-approval required | 0008 |

## Residual risks (accepted)
- **Trusted-tier plugins** run with the user's session. Mitigated only by admin approval,
  signatures, capability-bound approval, revocation and safe mode.
- **External-writer race (T5):** narrowed but **possibly undetectable and unrecoverable** for
  uncoordinated writers (POSIX has no mandatory locking). Coordinated writes through
  maintenance mode are safe.
- **Backups with active external writers** can capture torn or stale files. Maintenance mode
  does **not** prevent this, because it stops only PostelMD. Only pausing and draining the
  external writers (manifest `external_writers: paused`) gives a consistent backup (ADR-0017).
- **Plain HTTP on a LAN** (one-container installs) exposes credentials. Mitigated by a
  persistent UI warning, docs and optional self-signed TLS.
- **Single replica.** Short downtime during upgrades is accepted (RTO in ADR-0014).
