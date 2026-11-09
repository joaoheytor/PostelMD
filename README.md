# PostelMD

> A self-hosted, git-native Markdown workspace for operational knowledge
> (runbooks, ADRs, postmortems), built for DevOps, Platform and SRE engineers.

**Status:** pre-alpha. This repository contains design documents only and no code yet.
Start with the [roadmap](docs/roadmap.md).

## What it is

- **Plain files.** Your notes are `.md` files on disk and stay usable by git, vim and Syncthing.
- **Git-native.** Each vault is a git repository, with batched commits, push and history.
- **Safe by default.** Saves are conflict-aware: PostelMD clients never overwrite each other's
  work. External tools are safe when coordinated through maintenance mode. Secret
  detection runs on save and paste. Authentication is on from the first run.
- **Runs anywhere.** One `docker run` with one volume, Docker Compose or a Helm chart all use
  the same image.
- **Extensible.** A framework-agnostic plugin API with two trust tiers.

```sh
mkdir -p ~/postelmd
docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
  -p 127.0.0.1:8080:8080 --user "$(id -u):$(id -g)" -v ~/postelmd:/data \
  docker.io/joaoheytor/postelmd:0.1
docker logs postelmd   # prints a one-time setup URL
```

(Planned; see [ADR-0009](docs/adr/0009-zero-infra-distribution.md).)

## Documentation

| Doc | Purpose |
|---|---|
| [docs/PRD.md](docs/PRD.md) | Vision, personas, scope, non-goals, requirements |
| [docs/architecture.md](docs/architecture.md) | Components, data flow, protocols, deployment |
| [docs/adr/](docs/adr/) | Architecture Decision Records |
| [docs/threat-model.md](docs/threat-model.md) | STRIDE threat model |
| [docs/roadmap.md](docs/roadmap.md) | Milestones and acceptance criteria |
| [docs/ci-cd.md](docs/ci-cd.md) | Pipelines, repository settings, releases, homelab hand-off |
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
