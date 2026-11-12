# PostelMD

> A self-hosted, git-native Markdown workspace for operational knowledge
> (runbooks, ADRs, postmortems), built for DevOps, Platform and SRE engineers.

**Status:** v0.1, the single-user MVP, is code-complete; the first release candidate is
pending. It works today: one user with a password, one vault of `.md` files, an editor with
preview, conflict-aware autosave with drafts kept in the browser, create, move and delete
(into a trash folder), manual git commits, history and restoring a version, and adopting an
existing notes folder. Multi-user, auto-commit, push with secret gates and plugins come
later: see the [roadmap](docs/roadmap.md).

## What it is

In v0.1:

- **Plain files.** Your notes are `.md` files on disk and stay usable by git, vim and Syncthing.
- **The whole repository, not just notes.** YAML and JSON manifests, shell scripts, config
  files and code next to your notes open in the same editor, with syntax highlighting, and
  save byte for byte (line endings and byte order mark kept). Binary files are listed but
  don't open.
- **Git-native.** The vault is a git repository: you commit from the app, and browse and
  restore each note's history.
- **Safe by default.** Saves are conflict-aware: PostelMD clients never overwrite each other's
  work, and a change made outside PostelMD since the note was loaded opens the conflict view
  too (except in a narrow race with an external writer:
  [ADR-0003](docs/adr/0003-save-protocol.md)). A note that looks like it holds a secret gets a warning on save and paste, and
  the commit dialog checks the changes before they enter the history. Authentication is on
  from the first run.
- **One `docker run`** with one volume, from one multi-arch image (amd64 and arm64).

Later ([roadmap](docs/roadmap.md)): auto-commit, and push and pull with secret gates (v0.2);
several users and shared vaults, maintenance mode for coordinating external tools, and
Docker Compose and a Helm chart for the same image (v0.3); a plugin API with two trust tiers
(v0.4).

## Run it

```sh
mkdir -p ~/postelmd
docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
  --log-opt max-size=10m --log-opt max-file=3 \
  -p 127.0.0.1:8080:8080 --user "$(id -u):$(id -g)" -v ~/postelmd:/data \
  ghcr.io/joaoheytor/postelmd:0.1.0-rc.1
docker logs postelmd
```

Open http://localhost:8080/setup (the log prints this address; with another `-p` host port,
use yours) and paste the token from `docker logs postelmd`. The token expires after 30
minutes; `docker restart postelmd` prints a new one.

- **Which image.** Until v0.1.0 is released, use the newest release candidate,
  `ghcr.io/joaoheytor/postelmd:0.1.0-rc.N` (for testing, never for production).
  `ghcr.io/joaoheytor/postelmd:main` is the signed candidate built from every merge to
  `main`, and moves with each one. After the release, use
  `docker.io/joaoheytor/postelmd:0.1`. Tags: [ci-cd §4](docs/ci-cd.md#4-images-and-tags);
  verifying signatures: [verify-images](docs/operations/verify-images.md). Or build your own
  with `make image` (tagged `postelmd:dev`).
- **An existing notes folder:** add `-v ~/notes:/data/vaults/notes`. Setup adopts it (and
  makes it a git repository if it isn't one). Create the mount point first
  (`mkdir -p ~/postelmd/vaults/notes`), or Docker creates it owned by root.
- **Docker Desktop and WSL2:** run the command from a WSL shell and keep `~/postelmd` (and
  your notes) on the Linux filesystem, never under `/mnt/c`: Windows folders reach the
  container as 9p, which PostelMD refuses (ADR-0009).
- **macOS:** use a named volume, `-v postelmd-data:/data` without `--user` (ADR-0009).
- **A volume is required.** Without one, everything is lost at the next `docker rm` (every
  upgrade does one), so PostelMD refuses to start.

Backups, logs, upgrades and recovery: [running v0.1](docs/operations/running-v0.1.md).
Design: [ADR-0009](docs/adr/0009-zero-infra-distribution.md).

Settings are optional. They come from flags, then `POSTELMD_*` environment variables, then
`.postelmd/config.yaml` in the data directory (`~/postelmd/.postelmd/config.yaml` above), then
defaults. An unknown key in the file stops the server with its name, so a typo never goes
unnoticed:

```yaml
listen: ":8080"              # POSTELMD_LISTEN, --listen
allow_network_fs: false      # POSTELMD_ALLOW_NETWORK_FS; unsafe (ADR-0014)
allow_ephemeral_data: false  # POSTELMD_ALLOW_EPHEMERAL_DATA: start with no volume at /data
allowed_hosts: []            # POSTELMD_ALLOWED_HOSTS (comma-separated): host names besides
                             # localhost and IP addresses, e.g. [notes.example.org]
```

The data directory itself is set with `--data` or `POSTELMD_DATA_DIR`, never in the file.

- **Reaching PostelMD by a host name?** Add it to `allowed_hosts`. Any other name gets
  `421 Misdirected Request`: that stops a web page from rebinding its own name to your server
  (DNS rebinding).
- **Give PostelMD its own host name**, not a path under a name shared with other apps: its
  session cookie is scoped to the host. Behind a TLS proxy, see v0.2's trusted-proxy setting;
  v0.1 trusts no proxy headers.

## Documentation

| Doc | Purpose |
|---|---|
| [docs/PRD.md](docs/PRD.md) | Vision, personas, scope, non-goals, requirements |
| [docs/architecture.md](docs/architecture.md) | Components, data flow, protocols, deployment |
| [docs/adr/](docs/adr/) | Architecture Decision Records |
| [docs/threat-model.md](docs/threat-model.md) | STRIDE threat model |
| [docs/roadmap.md](docs/roadmap.md) | Milestones and acceptance criteria |
| [docs/ci-cd.md](docs/ci-cd.md) | Pipelines, repository settings, releases, homelab hand-off |
| [docs/operations/](docs/operations/) | Runbooks: [running v0.1](docs/operations/running-v0.1.md), [verifying images](docs/operations/verify-images.md) |
| [docs/lessons-from-frankmd.md](docs/lessons-from-frankmd.md) | Pitfalls this design avoids |
| [CLAUDE.md](CLAUDE.md) | Conventions for humans and AI coding agents |
| [CHANGELOG.md](CHANGELOG.md) | Release history ([Keep a Changelog](https://keepachangelog.com/en/1.1.0/)) |

## Name

The name honours [Jon Postel](https://en.wikipedia.org/wiki/Jon_Postel) (1943–1998), the
long-time RFC Editor who documented much of how the internet works.

## Provenance

PostelMD was **inspired by** [FrankMD](https://github.com/akitaonrails/FrankMD) by Fabio Akita
and his write-ups about building it. It is an independent, clean-room implementation: **no
FrankMD code is copied or forked.**

## License

[Apache-2.0](LICENSE). Contributions require a DCO sign-off (see
[CONTRIBUTING.md](CONTRIBUTING.md)).
