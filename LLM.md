# Hanzo Git — white-label Gitea fork

`hanzoai/git` is **Hanzo Git**: a white-label FORK of Gitea, pinned to the same
version git.hanzo.ai runs. It exists so git.hanzo.ai ships OUR branded image
(`ghcr.io/hanzoai/git`) instead of the stock upstream `gitea/gitea` image.

## Provenance / versioning (one-and-only-one-way)

- Fork base: upstream `go-gitea/gitea` tag **v1.24.7** — matched to live, never
  ahead. `git remote add upstream https://github.com/go-gitea/gitea.git`.
- Image tag: `1.24.7-hanzo.1` = `<upstream-version>-hanzo.<n>`. Bump the trailing
  `.n` for every rebuild at the same upstream version; bump the upstream prefix
  only when live upgrades. NEVER a bare `:latest`.
- Reported binary version comes from the root `VERSION` file (Makefile
  `STORED_VERSION_FILE`), so it is deterministic and git-independent in CI.

## What is rebranded (white-label per domain — git.hanzo.ai = Hanzo)

Only the user-visible brand + image identity change. Internal Go import paths
(`code.gitea.io/gitea`) are left untouched — they are invisible to users, and
renaming them across thousands of files would be pure churn.

- Marks: `public/assets/img/{logo,favicon,gitea}.svg`, `logo.png` (512),
  `favicon.png` + `apple-touch-icon.png` (180), and the `assets/{logo,favicon}.svg`
  sources → the canonical Hanzo block-H mark (from `hanzoai/brand`). Self-contained
  black tile + white H, so it reads on any theme.
- Name: default `APP_NAME` = "Hanzo Git" (`modules/setting/server.go` +
  `custom/conf/app.example.ini`) → drives every page `<title>` and the navbar.
- Image identity: OCI labels in `Dockerfile` (title/vendor/source/url = Hanzo).
- "Powered by Gitea" footer is config-gated off in the deployment
  (`SHOW_FOOTER_POWERED_BY=false`).

## Build (CI only — never local; NO github-hosted runners)

`.github/workflows/docker-publish.yml` builds `ghcr.io/hanzoai/git` on the
self-hosted `hanzo-build-linux-amd64` ARC pool (amd64; DOKS is all amd64),
triggered by pushing a `v*` tag. Self-contained (private repo → cannot call the
private reusable workflow). Upstream Gitea's own `.github/workflows/*` are
removed — a fork does not run Gitea's CI.

## Deploy

git.hanzo.ai runs this image via `universe/infra/k8s/gitea/deployment.yaml`
(namespace `hanzo`, deploy `gitea`, SQLite on the RETAINED `gitea-data` PVC,
replicas:1 + Recreate, hanzo.id OIDC login, Gitea Actions runners in
`universe/infra/k8s/gitea-runner`). Config is env-driven (`GITEA__*`); the
durable app.ini + DB live on the PVC and are NEVER recreated by a deploy.

## Branches

- `main` — the deployable Hanzo Git fork (this).
- `native-scaffold` — the SEPARATE long-term go-git + VFS→S3 + Base native
  rewrite vision (original 61f8e1b scaffold). Deferred track; NOT deployed here.
