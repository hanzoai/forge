# Hanzo Git — `github.com/hanzoai/git`

The rebranded **Gitea fork** that serves `git.hanzo.ai`: IAM-native code hosting
for the Hanzo / Lux / Zoo orgs, with native GitHub-Actions-compatible CI.

## What it is

- **Base:** Gitea **1.26.4** (upstream `go-gitea/gitea`; see `CHANGELOG.md` top
  entry). Module path forked to `github.com/hanzoai/git`; the Actions proto is
  `github.com/hanzo-git/actions-proto-go`. The daemon is **`gitd`**
  (`/app/gitd/gitd`, wrapper `/usr/local/bin/gitd`); upstream's CLI subcommands
  are intact under the new name (`gitd admin auth …`, `gitd migrate`).

  **`/usr/local/bin/gitd` is load-bearing, not cosmetic.** It is what
  `setting.AppPath` resolves to, and AppPath is written verbatim into every
  repository's `hooks/<hook>.d/gitd` delegate. The outer `hooks/<hook>` script
  runs EVERY executable in `<hook>.d/` and rejects the push if any exits
  non-zero, so moving that path — or leaving a delegate from an older binary
  name behind — breaks pushes. Both are handled automatically:
  `routers.syncAppConfForGit` re-runs `SyncRepositoryHooks` over every repo when
  AppPath changes, *before* the web listener opens, and `createDelegateHooks`
  deletes `legacyDelegateHookNames`. `gitd admin regenerate hooks` is the manual
  lever. Anything outside this repo that invokes the binary by absolute path —
  notably the `oauth-sync` init container in `hanzoai/universe` — must be
  updated in the SAME change that bumps the image tag.
- **`[actions]` intact:** `services/actions`, `models/actions`,
  `routers/api/actions/runner` — full runner registration + job API. Enabled
  via `GIT__actions__ENABLED=true` (also the source default,
  `modules/setting/actions.go`).

  **`WORKFLOW_DIRS` is ADDITIVE, and that is load-bearing.** The default is
  `.hanzo/workflows, .gitea/workflows, .github/workflows`
  (`modules/setting/actions.go:44`) and `modules/actions/workflows.go` scans
  ALL of them — it does not stop at the first that exists. So a repo mirrored
  in from GitHub has its `.github/workflows` executed HERE as well as on
  GitHub. For a repo whose `release.yml` owns a semver tag and pushes an
  image, that is a second releaser racing the first. Restricting the setting
  to `.hanzo/workflows` makes native CI opt-in by an in-repo directory, which
  is the only way to get one executor per repo. Repos that have adopted
  `.hanzo/workflows` (this one, universe, console, iam, platform, base, chat)
  are unaffected by that restriction; repos that have not would stop running
  here, which is the intent.
- **Identity = hanzo.id OIDC only.** No fork-baked issuer; binding is a standard
  Gitea OAuth2 auth source (goth `openidConnect`) pointed at
  `https://hanzo.id/.well-known/openid-configuration`. Org membership is driven by
  the IAM `owner` claim (`--group-claim-name owner --group-team-map …
  --group-team-map-removal`), reconciled declaratively by the deploy's `oauth-sync`
  init container. hanzo.id IAM app: `hanzo-gitea`.
- **Config = env.** `GIT__<section>__<KEY>` (upstream's app.ini API, kept
  verbatim under our prefix). No custom/conf baked, no Helm — the running config
  lives entirely in the deployment's env (see universe).
- **API is served at `/v1`, not `/api/v1`.** The deployed image answers
  `GET /v1/version` and `GET /v1/healthz`; `/api/v1/*` and `/api/healthz` are
  NOT served. The one surviving `/api/` path is the runner wire protocol
  (`POST /api/actions/runner.v1.RunnerService/*`), which is correct — the
  runner's connect client hardcodes it. Note the checkouts on a dev box may
  still mount `/api/v1` in `routers/init.go`; prod is ahead of that.
  Two 404 bodies discriminate: JSON `{"message":"not found"}` means the API
  handler ran and the object is absent; plaintext `404 page not found` means
  the route is not registered at all.
- **Runner management IS on the API**, under `…/actions/runners` at four
  scopes: `/v1/admin/actions/runners`, `/v1/orgs/{org}/actions/runners`,
  `/v1/repos/{owner}/{repo}/actions/runners`, `/v1/user/actions/runners`
  (each with `POST …/registration-token`). There is no "register a runner"
  endpoint — registration is runner-initiated over the connect RPC above,
  consuming a token from one of those endpoints, the web UI, or the
  `GIT_RUNNER_REGISTRATION_TOKEN` env bootstrap (`services/actions/init.go`).
- **KV, not redis.** The client is `github.com/hanzokv/go` and the vocabulary is
  KV end to end — there is no `redis` spelling left and no alias accepting one.
  Connection URIs are `kv://`, `kvs://`, `kv+socket://`, `kv+sentinel://`,
  `kv+cluster://` (the trailing `s` on `kv` is the one way to ask for TLS, so
  `kvs+sentinel://` / `kvs+cluster://` too). The operator-facing values are
  `[cache] ADAPTER = kv`, `[session] PROVIDER = kv`, `[queue] TYPE = kv`,
  `[global_lock] SERVICE_TYPE = kv`. `modules/nosql.getKVOptions` hand-parses the
  URI, so the scheme family is this repo's to define — it never reaches the
  client's own parser.

## Image / release lane

- Published as **`ghcr.io/hanzoai/git`** — v1-only, semver-pinned, never `:latest`.
  Upstream base is 1.26.4; releases run ahead of it (prod is on `v1.26.20`).
- **`.hanzo/workflows/cicd.yml` is THE lane** — a ~7-line caller of
  `hanzoai/ci/.github/workflows/build.yml@v1` with `secrets: inherit`. All real
  config lives in `/hanzo.yml` (one image `ghcr.io/hanzoai/git` from the root
  Dockerfile; one `go test ./modules/setting/...` gate). The hand-rolled
  `docker-release.yml` is gone, as are upstream Gitea's release workflows
  (Namespace.so runners / Docker Hub / S3 + GPG) — they targeted infra we do not
  have. NO GitHub-hosted builders.
- It does NOT roll out. `git.hanzo.ai` is pinned to an explicit tag by the `git`
  operator App CR in `hanzoai/universe`, and Hanzo CD runs with `selfHeal`, so a
  `kubectl patch` of the App does not stick. Recording the tag in universe is
  the only durable path, and for the service that hosts this repo that is
  deliberately a human decision — which is what `hanzo.yml` means by having no
  `deploy:` section.
- **Caveat on the `@v1` pin:** in `hanzoai/ci`, the `v1` tag and `main` are two
  parallel lineages sharing only the root commit. Their trees are identical
  today, so `@v1` and `@main` deliver the same `build.yml` — but future `main`
  commits will not reach `@v1` unless the tag is re-pointed.

## Where it runs

Operator-managed in `hanzoai/universe` (DOKS `hanzo-k8s`, namespace `hanzo`):

- `infra/k8s/operator/crs/git.yaml` — the `hanzo-git` App (this image), SQLite on
  the RWO `gitea-data` PVC, OIDC via the `oauth-sync` init container. Synced by
  Hanzo CD (ArgoCD; the App-only successor to the operator GitSource).
- `infra/k8s/git/` — the App's non-App supporting resources (Hanzo CD's project is
  `hanzo.ai/App`-only): `gitea-data` PVC, `hanzo-git-oauth` ConfigMap, the
  `git.hanzo.ai` Ingress, and `git-secrets-kms.yaml` (gitea-secrets from KMS).
- `infra/k8s/git-runner/` — the runner pool that executes Actions jobs. It is
  OUR image `ghcr.io/hanzoai/git-runner`, not upstream `gitea/act_runner`: a
  4-replica StatefulSet, privileged (DinD), `nodeSelector runner-pool: 32g`,
  registering against `http://hanzo-git.hanzo.svc` with the token from secret
  `git-runner-secrets/actions-runner-token`. `GIT_RUNNER_LABELS` maps ~30 labels
  (including `hanzo-build-linux-amd64` and every `ubuntu-*`) onto one image,
  `catthehacker/ubuntu:act-24.04` — so a workflow's `runs-on` selects a label,
  never a distinct environment. Two non-cluster runners (`evo-rocm`,
  `spark-cuda`) also register here for GPU jobs.
- **SSH is not reachable from outside the cluster.** The builtin server is on
  and binds `2222`, and `SSH_PORT=22` is what clone URLs advertise, but
  `service/hanzo-git` is ClusterIP and the `git.hanzo.ai` Ingress maps port 80
  only — there is no external listener on 22. Clone/push over HTTPS with a
  token works; `ssh://git@git.hanzo.ai` times out. An external LB/NodePort
  22→2222 is the outstanding piece.
- Push-to-deploy: a Gitea push webhook → cloud `/v1/git/webhook` → the `/v1/runner`
  build core. Architecture: `universe/docs/architecture/paas-in-cloud.md` §9.

## This fork is the serving plane; cloud's `/v1/git` is not

There are two native Git implementations, and the boundary between them is
settled by a rollback, not by preference:

- **This fork serves `git.hanzo.ai`** and drives Actions CI. It is the plane
  developers and runners actually use.
- **`hanzoai/cloud` `clients/git`** is a second, independent implementation
  mounted at `api.hanzo.ai/v1/git` (`/v1/git/health` answers). `git.hanzo.ai`
  was pointed at it and **rolled BACK to this fork on 2026-07-24**: repo URLs
  and every smart-HTTP shape 404'd, `git ls-remote` failed, only the SPA shell
  rendered. The reason is recorded at
  `hanzoai/universe: infra/k8s/git/ingress.yaml`, which also states the
  re-flip condition — cloud's `/v1/git` must serve clone + UI for the org
  paths again first.

Until that condition is met, treat this fork as canonical for hosting and
CI, and do not plan work that assumes the cutover has happened. The two are
already coupled in one direction: a push here fires a webhook to cloud
`/v1/git/webhook`, which is how push-to-deploy reaches the build core.
