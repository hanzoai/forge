# Hanzo Git

Hanzo's self-hosted Git service — a fork of [Gitea](https://gitea.com) made
IAM-native (single sign-on via [hanzo.id](https://hanzo.id)) and Hanzo-branded.
It powers [git.hanzo.ai](https://git.hanzo.ai).

## Relationship to upstream

Tracks upstream Gitea and carries a minimal Hanzo delta: branding (name, logo,
footer) and IAM-native OIDC login. The configuration contract is unchanged — the
server still reads `GITEA__<section>__<KEY>` environment variables, so this image
is a drop-in for existing Gitea tooling and Kubernetes manifests.

## Build & deploy

Built by the canonical Hanzo CI (`hanzo.yml` + `.github/workflows/cicd.yml`,
importing `hanzoai/ci`) to `ghcr.io/hanzoai/git`. Deployed via
[hanzoai/universe](https://github.com/hanzoai/universe) (`infra/k8s/git`).

## License

MIT, inherited from Gitea. See [LICENSE](LICENSE).
