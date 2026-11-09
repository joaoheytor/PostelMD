# ADR-0017: Backup and restore consistency

- **Status:** Proposed (v0.2: `backup`; v0.3: `restore` tested in CI)
- **Date:** 2026-11-09

## Context
An instance is made of things that must be recovered **together**:
- vault files and their `.git` directories;
- `app.db`: identity, memberships, file IDs, findings, plugin approvals and encrypted
  credentials;
- the key ring (`/data/.postelmd/keys/`, or keys supplied from outside).

A backup of the files without the database loses accounts and ACLs. A database without its
keys can't decrypt the stored credentials.

## Decision

### Consistency boundary
**One instance at one point in time**:
- `/data/vaults/**`;
- `/data/.postelmd/app.db`;
- the key ring;
- `config.yaml`;
- approved plugin artifacts;
- **`/data/.postelmd/recovery/`** and each vault's **`.postelmd-trash/`** (recovery copies and
  trashed items may be the only surviving version of user content referenced by open
  intents; their cleanup and purge are paused during capture).

Only derived data (the FTS index), caches and each vault's `.postelmd-tmp/` are excluded. The
index and caches are rebuilt on restore. Temp files are never the only copy of anything: an
interrupted save's content is still the client's draft, and recovery moves any temp worth
keeping into `recovery/` (ADR-0003 §5).

### Taking a backup (`postelmd backup` or the admin API)
1. Take the **instance backup barrier**. It covers **every mutation inside the consistency
   boundary**, not only vault writes:
   - vault content saves and git operations: wait for in-flight work (bounded by 30 s), then
     hold the vault locks;
   - key ring changes (rotation, retirement);
   - plugin install, update and removal;
   - `config.yaml` writes;
   - **recovery-area cleanup** (the 7-day expiry is suspended during capture);
   - account, membership and finding changes (they would otherwise only change the DB after
     the snapshot, which is harmless, but holding the barrier keeps the manifest simple).

   New mutations wait (≤ 5 s, then `503 Retry-After`; client drafts persist). Reads continue.
2. While holding the barrier:
   - run SQLite `VACUUM INTO` to a temporary file;
   - **copy the key ring and the plugin artifacts into the staging area.**

   SQLite guarantees only that the database copy is internally consistent. Taking the files in
   the same barrier window is what makes the database, keys and artifacts agree with each
   other.
3. Write the vault files (including `.git`) into the archive while still holding the barrier,
   then add `config.yaml` and a `manifest.json` (instance ID, app version, schema version,
   timestamp, the key IDs referenced by the database, per-file checksums). Before releasing,
   verify that every key ID the database references is present in the staged key ring (or
   listed as external), and that **every open intent's recovery files are in the archive**.
   Otherwise the backup fails.
4. Release the barrier. The total time with writes paused is roughly proportional to the vault
   size.

### Volume snapshots as an alternative
For large instances, storage-level snapshots (for example Longhorn) can replace the archive
step. Startup repair (ADR-0003 §2, ADR-0016) then handles any torn state. This is only valid
under one of these conditions:
- **Verified single volume:** at startup, PostelMD records whether `/data/.postelmd` and every
  vault path sit on the **same mount** (same device ID, no mount point below `/data`). The
  backup UI and `postelmd backup --plan` show the layout, and offer a snapshot-only backup only
  when it is single-volume.
- **Several backing volumes** (for example vaults mounted separately, per ADR-0009): every
  volume is snapshotted **inside the barrier**. `postelmd backup --hold <max-duration>`
  takes the barrier, prints the list of mounts to snapshot, and keeps the barrier until the
  operator or tool calls `--release`, or the maximum duration expires (then the backup is
  marked failed). A snapshot set taken outside the barrier, or missing a volume, is not a
  valid backup.

### What the barrier cannot cover
The barrier stops PostelMD, not external writers (Syncthing, editors, an outside `git`). A file
an external tool changes while the vaults are being archived may be captured torn or in an
older state, and **reconciliation after restore cannot reconstruct bytes the backup never
saw**. **Maintenance mode does not help here**: it stops PostelMD and deliberately lets
external tools write.

There are two guarantee levels, recorded in the manifest as `external_writers`:
- **`paused`** (consistent): before the backup, the operator pauses **and drains** every
  external writer (for example, pause the Syncthing folders through its API and wait until they
  are idle, or scale the sidecar to 0). The backup job can do this automatically for a
  configured Syncthing API.
- **`unknown`** (weaker; the default for scheduled backups): files written by external tools
  during capture may be torn or stale. PostelMD-managed content is still consistent. The docs
  and the manifest state this plainly.

### Encryption and keys
- Archives are encrypted with **age** to a recipient public key (`POSTELMD_BACKUP_RECIPIENT`).
  The private key never lives on the instance.
- The key ring is **included** in the encrypted archive by default, so that one artifact plus
  the age identity is enough to restore.
  - Keys supplied externally (a K8s Secret or `_FILE`) are **not** included. The manifest
    lists the key IDs the restore will need, and the docs require backing those up separately
    (for the homelab, the Sealed Secrets controller key; see the homelab
    `docs/sealed-secrets-key-recovery.md`).
- Unencrypted backups require `--insecure-plaintext` and print a warning.

### Retention (defaults for the docs; the homelab reference is in ADR-0014)
24 hourly, 14 daily and 8 weekly copies. Retention is enforced by the destination
(object-storage lifecycle rules) rather than by PostelMD.

### Restore (`postelmd restore <archive>`)
- **Only onto an empty `/data`.** There is no in-place replacement: moving `/data` aside doesn't
  work when `/data` is a mounted volume inside a container with a read-only root filesystem.
  The procedure (`docs/operations/restore.md`):
  1. stop the instance;
  2. provision a **new, empty volume** (a new PVC or Docker volume);
  3. run `postelmd restore` with the new volume mounted at `/data`;
  4. switch the deployment to the new volume.

  The old volume stays untouched as the rollback path until the admin deletes it.
- Steps:
  1. verify the checksums;
  2. check the schema version (newer → refuse; older → run migrations per ADR-0015);
  3. unpack;
  4. check that the required keys are available;
  5. rebuild the index;
  6. run a full reconcile;
  7. invalidate all sessions (ADR-0018);
  8. start in **quarantine**, which trusts **no restored identity**, admins included (an
     admin may have been suspended after the backup):
     - `postelmd restore` **requires** a `--recovery-admin <name>` together with
       `--recovery-password-file`, or prints a one-time setup token for it. This creates a
       **recovery identity** controlled by whoever runs the restore: the operator with
       volume and CLI access. It is a separate account, marked `recovery`;
     - **every** restored account, **including restored admins**, is `pending-revalidation`
       and can't log in, even with a valid password or passkey;
     - only the recovery identity can log in;
     - git push/pull, webhooks and plugin network access are disabled;
     - **pending tokens from the backup are purged**: invites, reset links and setup tokens.
       They were valid at backup time but may have been revoked since, and the external audit
       sink can't prove otherwise. Admins issue new ones after the quarantine;
     - the instance starts in **plugin safe mode** (ADR-0008): every plugin stays disabled
       until the recovery admin re-enables it, since an approval may have been withdrawn after
       the backup;
     - the UI shows the revalidation checklist and the report from the external audit sink:
       the security events after the backup timestamp (suspensions, membership removals, key
       rotations, finding acknowledgements);
     - the recovery admin re-applies those events and approves accounts. **Restored admins are
       approved individually**, never in bulk, and their admin flag is reviewed explicitly.
       Then the recovery admin ends the quarantine (audit-logged);
     - ending the quarantine **disables the recovery identity**. It can be kept only by
       converting it into a normal named account.
  - **If the external audit records are unavailable** for any part of the period since the
    backup, the report says so explicitly. Then **no** account is released in bulk:
    each one needs individual approval, and the admin is prompted to rotate the git remote
    credentials.
- The restored instance keeps its instance ID.
- **Restore tests:** CI restores a fixture backup onto an empty instance on every release. The
  homelab runs a monthly restore drill into a scratch namespace.

### Out-of-band documentation
Backup and restore runbooks live in `docs/operations/`, are shipped in the release archive and
are linked from the chart's NOTES output, so they are reachable while PostelMD is down. They
are **never only inside a PostelMD vault**.

## Consequences
- Positive: one encrypted artifact plus one external identity restores the whole instance.
- Negative: brief write pauses during a logical backup; external keys must be backed up
  separately.

## Alternatives considered
| Option | Why not |
|---|---|
| SQLite online backup + tar without quiescence | Database and files from different moments; references can dangle |
| Volume snapshots only | Doesn't help Docker or binary users; not portable across storage |
| Rely on git remotes as backup | Misses identity, ACLs, findings and uncommitted work |
