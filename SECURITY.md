# Security Policy

## Supported versions

PostelMD is pre-1.0. Only the latest release receives security fixes. From 1.0 on, the latest
minor release and the previous one are supported.

## Reporting a vulnerability

**Do not open a public issue.** Report it privately through GitHub's
**Security → Report a vulnerability** (Private Vulnerability Reporting) on the repository.

Please include the affected version, the configuration (`docker run` and its flags), steps to
reproduce, the impact, and a proof of concept if you have one.

## What to expect

| Step | Target |
|---|---|
| Acknowledgement | 3 business days |
| Initial assessment | 10 business days |
| Fix or mitigation for high/critical issues | 30 days |
| Coordinated disclosure | Up to 90 days after the report, or earlier once a fix ships |

Fixes are published as GitHub Security Advisories, with CVEs where applicable. Reporters are
credited unless they ask not to be.

## Scope

In scope: the PostelMD server and web UI, the official container images
(`ghcr.io/joaoheytor/postelmd` and `docker.io/joaoheytor/postelmd`), and the release
workflows that build and sign them. v0.1 has no Helm chart, binaries or plugin API: they come
later ([roadmap](docs/roadmap.md)), and each joins this scope when it first ships, along with
the `@postelmd/plugin-api` package. Third-party plugins will be out of scope; report those to
their authors (and to us if the plugin API itself is at fault).

The design-level security model is described in [docs/threat-model.md](docs/threat-model.md).
