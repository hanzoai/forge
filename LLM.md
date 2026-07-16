# Hanzo Git — `github.com/hanzoai/git`

The rebranded **Gitea fork** that serves `git.hanzo.ai`: IAM-native code hosting
for the Hanzo / Lux / Zoo orgs, with native GitHub-Actions-compatible CI.

## What it is

- **Base:** Gitea **1.26.4** (upstream `go-gitea/gitea`; see `CHANGELOG.md` top
  entry). Module path forked to `github.com/hanzoai/git`; the Actions proto is
  `github.com/hanzo-git/actions-proto-go`. Binary is still `gitea`
  (`/app/gitea/gitea`), CLI subcommands intact (`gitea admin auth …`).
- **`[actions]` intact:** `services/actions`, `models/actions`,
  `routers/api/actions/runner` — full act_runner registration + job API. Enabled
  via `GITEA__actions__ENABLED=true`.
- **Identity = hanzo.id OIDC only.** No fork-baked issuer; binding is a standard
  Gitea OAuth2 auth source (goth `openidConnect`) pointed at
  `https://hanzo.id/.well-known/openid-configuration`. Org membership is driven by
  the IAM `owner` claim (`--group-claim-name owner --group-team-map …
  --group-team-map-removal`), reconciled declaratively by the deploy's `oauth-sync`
  init container. hanzo.id IAM app: `hanzo-gitea`.
- **Config = env.** `GITEA__<section>__<KEY>` (upstream's app.ini API, kept
  verbatim). No custom/conf baked, no Helm — the running config lives entirely in
  the deployment's env (see universe).

## Image / release lane

- Published as **`ghcr.io/hanzoai/git`** — v1-only, semver-pinned, never `:latest`.
  First release **`1.26.5`** (next patch over the upstream base 1.26.4).
- `.github/workflows/docker-release.yml` is THE lane: on a `v1.*` tag it builds
  `linux/amd64` on the self-hosted **`hanzo-build-linux-amd64`** ARC pool and pushes
  to GHCR with `GITHUB_TOKEN` (semver tag via `docker/metadata-action`). NO
  GitHub-hosted builders. It REPLACES upstream Gitea's release workflows
  (Namespace.so runners / Docker Hub / `go-gitea/gitea` / S3 + GPG), which were
  removed — they targeted infra we do not have and would fail on every tag/push.
- Cut a release: `git tag v1.26.5 && git push origin v1.26.5` → image
  `ghcr.io/hanzoai/git:1.26.5`.

## Where it runs

Operator-managed in `hanzoai/universe` (DOKS `hanzo-k8s`, namespace `hanzo`):

- `infra/k8s/operator/crs/git.yaml` — the `hanzo-git` App (this image), SQLite on
  the RWO `gitea-data` PVC, OIDC via the `oauth-sync` init container.
- `infra/k8s/git-runner/` — the act_runner DinD pool (upstream
  `gitea/act_runner:0.6.1-dind`) that runs Actions jobs; maps `hanzo-build-linux-amd64`.
- `crs/git-ingress.yaml` — `git.hanzo.ai` ingress (staged; cut by the coordinator).
- Push-to-deploy: a Gitea push webhook → cloud `/v1/git/webhook` → the `/v1/runner`
  build core. Architecture: `universe/docs/architecture/paas-in-cloud.md` §9.

The migration (fork becomes THE git server, replacing the raw upstream-image deploy
and the cloud embedded git seam as the host) is STAGED — the coordinator flips it.
