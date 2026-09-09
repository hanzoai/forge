> **Retired — this is a stale copy of `hanzoai/git`.**
>
> Every branch here is reachable from `hanzoai/git` — 12 refs, not one commit it lacks — and this copy has no push mirror, so anything committed here reached nothing.
>
> It also declared `ghcr.io/hanzoai/git`, the tag `hanzoai/git` owns, so a push here
> could have published over it. That declaration is removed.

# Hanzo Forge

[![](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT "License: MIT")

Hanzo's self-hosted Git forge — Git hosting, code review, issues, packages, and
CI/CD (GitHub-Actions-compatible), IAM-native via hanzo.id OIDC. Runs at
[git.hanzo.ai](https://git.hanzo.ai).

A white-label fork of [Gitea](https://gitea.com) (MIT). Upstream copyright and
licensing are preserved in [LICENSE](LICENSE); identity is wired to Hanzo IAM
rather than local accounts, and the product is branded Hanzo Forge.

## Build & deploy

One way, like every Hanzo repo: the root [`hanzo.yml`](hanzo.yml) declares the
image and tests; a ~7-line [`.hanzo/workflows/cicd.yml`](.hanzo/workflows/cicd.yml)
imports [`hanzoai/ci`](https://github.com/hanzoai/ci), which builds and pushes
`ghcr.io/hanzoai/git` on our own runners against git.hanzo.ai. Never build the image locally.

The running service is defined in `hanzoai/universe` (the `git` operator App CR):
a single-writer SQLite deployment, config injected via the `GIT__<section>__<KEY>`
env contract, OIDC login reconciled to hanzo.id.

## Development

See [docs/development.md](docs/development.md) for a local environment.
After building, run `./gitd web` to start the server, or `./gitd help` for
all commands.
