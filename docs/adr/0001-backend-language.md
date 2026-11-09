# ADR-0001: Go backend, TypeScript + CodeMirror 6 frontend

- **Status:** Proposed
- **Date:** 2026-11-09

## Context
We need a self-hosted, multi-user, plugin-capable editor that:
- runs from a single `docker run`, from a Helm chart on a mixed arm64/amd64 cluster, and as a
  plain binary;
- is maintained mostly by one person with AI assistance;
- targets DevOps, Platform and SRE engineers.

Two independent architecture reviews (a senior software and infrastructure view, and an
enterprise and operations view) compared TypeScript (Node/Bun), Go and Rust over four rounds:
the base requirements, then plugins and open source, then the single-`docker run`
requirement, then the DevOps/SRE audience. Every round ranked Go > TS > Rust.

## Decision
- **Backend:** Go, stdlib `net/http` (1.22+ route patterns), `CGO_ENABLED=0`, a single static
  binary that embeds the frontend with `go:embed`.
- **Frontend:** TypeScript, Vite and CodeMirror 6. A framework-free `core/` holds state. The UI
  shell framework will be chosen by a spike and recorded in a later ADR, and must never
  leak into the plugin API.
- **API contract:** OpenAPI. Go handlers and TS types are generated or checked against it.

## Consequences
- Positive:
  - A ~20 MB distroless image with low memory use and trivial cross-compilation for arm64 and
    amd64.
  - `os.Root` for path confinement.
  - Strong supply-chain posture.
  - Native libraries for the audience's features (go-git, gitleaks, kubeconform, the
    Prometheus client).
  - The skills carry over to K8s work.
- Negative: two languages. The server parser (goldmark, used only for extraction) and the
  client parser (markdown-it, used for rendering) can diverge; shared golden fixtures mitigate
  that (ADR-0007).
- Plugin authors write TS/JS regardless of the backend. Server plugins, when added, use WASM
  (ADR-0008).

## Alternatives considered
| Option | Why not |
|---|---|
| TypeScript full-stack (Node/Bun) | Close second: one language and one parser. Loses on image size, memory, npm supply-chain risk, and native modules per architecture. Node `vm` is not a plugin sandbox anyway. |
| Rust | Smallest footprint and best compile-time safety, but the slowest path to an MVP for a solo learner, and AI agents struggle with lifetimes and async. Reconsider if a desktop-first (Tauri) path becomes the priority. |
| Rails (as FrankMD) | Its author regretted it (no desktop path), and it is heavier to operate. |

**This decision flips** to TS if a browser-only, local-first mode becomes the priority, and to
Rust if desktop-first becomes the priority.
