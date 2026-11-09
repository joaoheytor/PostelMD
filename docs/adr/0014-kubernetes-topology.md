# ADR-0014: Single writer: Kubernetes topology and instance lock

- **Status:** Proposed
- **Date:** 2026-11-09 (revised after design review)

## Context
The maintainer runs a 4-node kubeadm cluster (K8s 1.34, arm64 + amd64) over Tailscale with
Argo CD, Longhorn, cert-manager, Sealed Secrets, Harbor and VictoriaMetrics. PostelMD relies on
**exactly one process** owning `/data`: in-memory locks, SQLite, the watcher and the save
protocol (ADR-0003) all assume it. Kubernetes alone doesn't guarantee this:
- `ReadWriteOnce` allows several pods **on the same node** to mount the volume;
- force-deleting a StatefulSet pod can start a replacement while the old container still runs
  (for example on a partitioned node);
- under plain Docker, nothing stops two containers from mounting the same directory.

## Decision

### 1. Application-level instance lock (all install paths)
- On startup, before opening SQLite or watching anything, the process takes an **exclusive,
  non-blocking `flock`** on `/data/.postelmd/instance.lock` and keeps it for its lifetime. It
  writes its hostname, PID, start time and a boot UUID into the file.
- If the lock is held, the process **refuses to start**: it exits non-zero with an error naming
  the current holder. A held lock is never broken automatically.
- The boot UUID is stored in SQLite at startup. Background workers check it periodically, and
  if it changed (a second writer slipped past, for example on a filesystem where `flock` is
  emulated), the process **stops writing** and exits, raising `postelmd_split_brain_total`.
- Startup refuses network filesystems (NFS, SMB, CIFS, FUSE-backed) for `/data/.postelmd`,
  unless `POSTELMD_ALLOW_NETWORK_FS=true`. That variable is documented as unsafe.

### 2. Kubernetes topology
- **StatefulSet with `replicas: 1`.**
- **PVC with `accessModes: [ReadWriteOncePod]`** (GA since K8s 1.29; supported by Longhorn's
  CSI driver). It guarantees a single pod, even on the same node. The chart falls back to
  `ReadWriteOnce` only behind an explicit value with a warning in the chart's NOTES output.
- **No RWX or NFS**: inotify doesn't see writes from other NFS clients, and SQLite locking is
  unsafe on NFS.
- Pod security:
  - `runAsNonRoot` with UID/GID 65532 and an `fsGroup`;
  - `readOnlyRootFilesystem: true` (with an `emptyDir` at `/tmp`);
  - all capabilities dropped;
  - restricted Pod Security Standard;
  - a NetworkPolicy allowing traffic only from the ingress and the metrics scraper.
- Probes:
  - `startupProbe` covers the first indexing;
  - liveness: `/healthz`;
  - readiness: `/readyz` (index loaded and initial reconcile done).
- **Shutdown:** `terminationGracePeriodSeconds: 60`. The shutdown sequence has a hard
  internal deadline of 45 s, documented in architecture §8. Anything not flushed by then is
  safe because it is either already durable (the outbox) or still held as a client draft.
- Optional **Syncthing sidecar** mounting the same PVC: inotify works because it is the same
  kernel, and its `.sync-conflict-*` files are surfaced in the UI.
- **Helm chart** values:
  - `existingSecret`;
  - `persistence.storageClass`;
  - `persistence.accessMode`;
  - `ingress` / `httpRoute` toggles;
  - `serviceMonitor`;
  - `trustedProxies`;
  - `publicURL`;
  - `oidc.*`.

### 3. Operator runbook: replacing a stuck pod
**Never `kubectl delete pod --force`** unless the old writer is known to be stopped.
**Unreachable is not stopped:** a partitioned node can keep running the old container and
keep writing to its attached volume. Deleting the Node object proves nothing about the
process. Before forcing, establish **one** of these:
1. **Confirmed power-off.** The machine is verified off through an out-of-band channel (the
   hypervisor or VPS console, IPMI, physically unplugged). Then apply the
   `node.kubernetes.io/out-of-service=nodeshutdown:NoExecute` taint (the Kubernetes
   non-graceful node shutdown procedure). Only then force-delete.
2. **Effective storage fencing.** The volume is detached at the storage layer **and** the old
   node can no longer write to it. For Longhorn: the engine on the old node has been stopped,
   or its replicas are unreachable from that node, and the volume shows as detached from it.
   A detach that only shows in `volumeattachment` while the node may still have the block
   device mounted does **not** count.

If neither can be established, **wait** (the RTO is exceeded, the data is intact) rather than
risk two writers. The new pod's instance lock is a final check, but it cannot see a writer on
another node. These steps are what prevents that.

The runbook is shipped in `docs/operations/` and in the chart's NOTES output, so it stays
available when PostelMD is down.

### 4. Homelab integration (in the `homelab` repo, not here)
- An Argo CD Application in the App-of-Apps, with a sync wave after Sealed Secrets and
  cert-manager.
- The admin password and keys come from a SealedSecret.
- The image is pulled through Harbor.
- Access over Tailscale.

### 5. Recovery objectives (homelab reference; defaults for the docs)
| Scenario | RPO | RTO | Mechanism |
|---|---|---|---|
| Pod or node restart | 0 (saves are durable on return) | < 1 min | StatefulSet reschedules; the instance lock ensures a single writer |
| Volume loss (Longhorn replicas lost) | ≤ 1 h | < 30 min | Hourly Longhorn backup to the off-site target, plus git remotes |
| Cluster loss | ≤ 24 h (≤ 1 h for git-pushed vaults) | < 2 h | Off-site backup + `postelmd restore` onto a fresh install (ADR-0017) |

Retention: 24 hourly, 14 daily and 8 weekly copies. A restore test runs monthly. Details are in
ADR-0017.

## Consequences
- Positive: duplicate writers are rejected on every install path, not only in K8s. The
  recovery procedure is written down.
- Negative: no HA. Operators must follow the fencing runbook before forcing replacement.

## Alternatives considered
| Option | Why not |
|---|---|
| Rely on StatefulSet semantics only | Violated by force deletion; RWO allows several pods on the same node |
| Leader election via the K8s Lease API | Only works in K8s, not under `docker run`; a file lock covers every case |
| Multiple replicas + RWX | Duplicate watchers, distributed locks, SQLite on NFS: a distributed system for a handful of users |
