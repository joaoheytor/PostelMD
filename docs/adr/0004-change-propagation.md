# ADR-0004: Change propagation: watcher + reconciler + SSE

- **Status:** Proposed
- **Date:** 2026-11-09

## Context
Notes change outside the app (`git pull`, vim, Syncthing, another user). FrankMD only noticed
on the next save. inotify is unreliable in some environments:
- NFS does not deliver events from other clients;
- Docker Desktop on macOS and Windows often drops bind-mount events;
- editors save through temp-file renames.

## Decision
- **fsnotify** watches each vault directory recursively (one watch per directory). Events are
  **hints**: they are debounced and coalesced per path.
- A **reconciler** is the source of truth for changes. It runs a full scan at startup and every
  `POSTELMD_RECONCILE_INTERVAL` (default 5 min), comparing mtime, size and hash with the
  metadata cache. It is mandatory, not optional.
  - It may **scan** without locks, but it **applies** its findings (new IDs, generation bumps,
    tombstones) only under the **vault lock** (ADR-0003 §4), re-checking each path under the
    lock first. So it never acts on the middle of a structural operation, a save or a git
    operation.
  - It **skips every path named by an open or flagged intent** (save, structural, restore or
    git), and the reserved paths (ADR-0006). Those paths belong to their intent's recovery
    table, or to the user's decision, until the intent is cleared.
- **Startup order.** Each step finishes before the next starts; nothing serves requests or
  mutates a vault before intent recovery has run:
  1. take the instance lock (ADR-0014);
  2. refuse to start if a restore is in progress (ADR-0017; the marker is specified in #4);
  3. run schema migrations (ADR-0015);
  4. write the `post_migration_started` marker if a migration ran (ADR-0015);
  5. **intent recovery** for every open intent (save, structural, restore, git; ADR-0003 §2
     and §5, ADR-0016), then the temp sweep (ADR-0003 §5);
  6. start the watcher, holding its hints, then run the **full reconcile** (with the rules
     above), then apply the held hints;
  7. replay the unprocessed outbox (ADR-0003 §6);
  8. start the background workers (index, commit, push, purge);
  9. open the admin socket, then HTTP. `/readyz` reports ready only after this step.
- Changes go to a **per-vault event bus** and are pushed to clients over **SSE**
  (`GET /api/v1/events`):
  - each event has a monotonic ID, and a ring buffer replays from `Last-Event-ID`;
  - if the requested ID is outside the buffer, the server sends a `resync` event;
  - responses set `X-Accel-Buffering: no` and send a 15 s heartbeat.
- The server's own writes are **published explicitly from the outbox** (ADR-0003 §5). Echo
  suppression only discards the watcher's duplicate hint, never the application event.
- **Stream contract:**
  - cursors carry the server's **boot epoch**; an epoch mismatch forces `resync`;
  - every event is authorized when it is delivered, and replay is re-authorized (ADR-0018);
  - slow consumers are disconnected with `resync`;
  - clients **open the stream first** and record its head cursor, then load the snapshot, then
    apply events after that cursor, de-duplicated by `(fileID, generation)` (architecture §5).
- Large bursts (for example `git checkout` touching 10k files) trigger a reconcile pass instead
  of per-file events.
- The status page shows "watch degraded" when the reconciler finds drift the watcher missed.

## Consequences
- Positive: correct even where inotify is broken; works behind proxies; simple client.
- Negative: changes can be delayed up to the reconcile interval on degraded mounts.
- Metrics: `postelmd_watcher_lag_seconds`, `postelmd_reconcile_drift_total`.
- Ops note: check `fs.inotify.max_user_watches` on the nodes.

## Alternatives considered
| Option | Why not |
|---|---|
| WebSocket | Bidirectional traffic is not needed; SSE reconnects and replays natively and is friendlier to proxies |
| Polling from the client | Wasteful; poor UX |
| Watcher only | Misses events on NFS and Docker Desktop |
