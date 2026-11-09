# ADR-0012: License: Apache-2.0 with DCO

- **Status:** Accepted (2026-11-09, with the `LICENSE` file in the first commit)
- **Date:** 2026-11-09

## Context
PostelMD is public from its first commit and targets DevOps, Platform and SRE engineers, who often
evaluate tools inside companies where legal teams approve licenses. A plugin ecosystem needs
licensing that third-party authors can rely on.

| | MIT | **Apache-2.0** | AGPL-3.0 |
|---|---|---|---|
| Closed forks or hosted SaaS by others | Allowed | Allowed | Must publish source if offered over a network |
| Explicit patent grant | ❌ | ✅ | ✅ |
| Trademark (name) protection | Silent | ✅ explicitly not granted | Silent |
| Contribution terms built in | ❌ | ✅ (§5) | — |
| Corporate acceptance | High | **Highest**; CNCF standard | Often banned |
| Plugin ecosystem clarity | Clear | Clear | Ambiguous (derivative works) |

## Decision
- **Apache-2.0** for everything: server, web app and `@postelmd/plugin-api`.
- **DCO** (`Signed-off-by:`), enforced by a GitHub app. No CLA.
- `NOTICE` file if third-party notices require it.

## Consequences
- Positive: maximum adoption by the target audience; patent and trademark clarity; plugin
  authors may choose any license.
- Negative: someone could offer a closed hosted fork. This is accepted as low risk for a
  self-hosted notes tool.
- Escape hatch: a future relicense to AGPL for new versions (as Grafana did) remains
  possible for code we own. Past releases stay Apache-2.0.

## Alternatives considered
| Option | Why not |
|---|---|
| MIT | No patent grant; less precise |
| AGPL-3.0 (+ Apache SDK) | Protects against SaaS forks but hurts corporate adoption |
| CLA | Friction for contributors; not needed without a relicensing plan |
