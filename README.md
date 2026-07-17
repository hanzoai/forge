# Hanzo Git

[![](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT "License: MIT")

Hanzo's self-hosted Git forge — Git hosting, code review, issues, packages, and
CI/CD (GitHub-Actions-compatible), IAM-native via hanzo.id OIDC. Runs at
[git.hanzo.ai](https://git.hanzo.ai).

A white-label fork of [Gitea](https://gitea.com) (MIT). Upstream copyright and
licensing are preserved in [LICENSE](LICENSE); identity is wired to Hanzo IAM
rather than local accounts, and the product is branded Hanzo Git.

## Build & deploy

One way, like every Hanzo repo: the root [`hanzo.yml`](hanzo.yml) declares the
image and tests; a ~7-line [`.github/workflows/cicd.yml`](.github/workflows/cicd.yml)
imports [`hanzoai/ci`](https://github.com/hanzoai/ci), which builds and pushes
`ghcr.io/hanzoai/git` on the arc pool. Never build the image locally.

The running service is defined in `hanzoai/universe` (the `git` operator App CR):
a single-writer SQLite deployment, config injected via the `GIT__<section>__<KEY>`
env contract, OIDC login reconciled to hanzo.id.

## Development

See [docs/development.md](docs/development.md) for a local environment.
After building, run `./gitea web` to start the server, or `./gitea help` for
all commands.
