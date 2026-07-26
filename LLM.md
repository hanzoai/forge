# Hanzo Git — `github.com/hanzoai/git`

The rebranded **Gitea fork** that serves `git.hanzo.ai`: IAM-native code hosting
for the Hanzo / Lux / Zoo orgs, with native GitHub-Actions-compatible CI.

## What it is

- **Base:** Gitea **1.26.4** (upstream `go-gitea/gitea`; see `CHANGELOG.md` top
  entry). Module path forked to `github.com/hanzoai/git`; the Actions proto is
  `github.com/hanzo-git/actions-proto-go`. The daemon is **`gitd`**
  (`/app/git/gitd`, wrapper `/usr/local/bin/gitd`); upstream's CLI subcommands
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
  `routers/api/actions/runner` — full act_runner registration + job API. Enabled
  via `GIT__actions__ENABLED=true`.
- **Identity = hanzo.id OIDC only.** No fork-baked issuer; binding is a standard
  Gitea OAuth2 auth source (goth `openidConnect`) pointed at
  `https://hanzo.id/.well-known/openid-configuration`. Org membership is driven by
  the IAM `owner` claim (`--group-claim-name owner --group-team-map …
  --group-team-map-removal`), reconciled declaratively by the deploy's `oauth-sync`
  init container. hanzo.id IAM app: `hanzo-gitea`.
- **Config = env.** `GIT__<section>__<KEY>` (upstream's app.ini API under our
  prefix; `modules/setting.EnvConfigKeyPrefixGit`). `GITEA__*` is NOT accepted —
  there is no fallback, so a stale `GITEA__` var is silently ignored. No
  custom/conf baked, no Helm — the running config lives entirely in the
  deployment's env (see universe).
- **KV, not redis.** The client is `github.com/hanzokv/go` and the vocabulary is
  KV end to end — there is no `redis` spelling left and no alias accepting one.
  Connection URIs are `kv://`, `kvs://`, `kv+socket://`, `kv+sentinel://`,
  `kv+cluster://` (the trailing `s` on `kv` is the one way to ask for TLS, so
  `kvs+sentinel://` / `kvs+cluster://` too). The operator-facing values are
  `[cache] ADAPTER = kv`, `[session] PROVIDER = kv`, `[queue] TYPE = kv`,
  `[global_lock] SERVICE_TYPE = kv`. `modules/nosql.getKVOptions` hand-parses the
  URI, so the scheme family is this repo's to define — it never reaches the
  client's own parser.

## Upstream naming: what stays, and why

The rendered UI, locale strings, CLI help, log/error text, outbound User-Agents
and our own `X-*` headers are Hanzo Git. What is left is left on purpose — it is
either legally required or an addressing value where a rename silently points at
a different resource. Do NOT sed these:

- **MIT attribution** — `Copyright ... The Gitea Authors` headers, `LICENSE`,
  and the generated `licenses.txt` (17 hits, all dependency notices). Required.
- **Dependency import paths** — `gitea.com/go-chi/*`, `gitea.com/gitea/runner/*`,
  and `github.com/go-gitea/gitea` links in provenance comments.
- **Addressing values.** Renaming these redirects to a resource that does not
  exist, usually silently: webhook type `gitea` (DB column + API enum + the
  `/settings/hooks/gitea/*` route and its templates), migration source service
  `gitea`, `.gitea/` repo conventions (workflows, issue/PR templates — users'
  own files), bleve analyzer `gitea/path`, indexer names `gitea_issues` /
  `gitea_codes`, the `[storage]` S3/Azure default `gitea`, the user setting key
  `email_notification.gitea_actions`, and `yaml:"gitea"` markdown front-matter.
- **`/data/gitea`, `/etc/gitea`, `gitea.db`** — the durable volume layout. A
  rename is a live PVC migration, not a branding change. `/app/git/gitd` and
  `/usr/local/bin/gitd` (the binary) are already ours.
- **`WWW-Authenticate: Basic realm="Gitea"`** in `routers/web/repo/githttp.go` —
  Git Credential Manager matches this literal to offer built-in OAuth2
  (git-ecosystem/git-credential-manager#1442). The OAuth2-disabled branch beside
  it is ours, because its job is to NOT match that probe.
- **`ONLY_ALLOW_PUSH_IF_GITEA_ENVIRONMENT_SET`** — renaming the key makes any
  app.ini that set it `false` silently revert to the `true` default, which
  rejects pushes. Needs a migration, not a rename.

Still branded upstream and needing an owner decision, because each is a wire
contract with consumers we cannot enumerate: the `X-GITEA-OTP` request header
(`services/auth/basic.go`, CORS allow-list, both swagger specs), the webhook
delivery headers `X-Gitea-{Delivery,Event,Event-Type,Signature,Hook-Installation-Target-Type}`
(`services/webhook/deliver.go`), and the notification-mail headers `X-Gitea-*`
(`services/mailer/`). All three already ship vendor-compat aliases beside them
(`X-GitHub-*`, `X-Hub-Signature-256`, `X-Gogs-*`, `X-GitLab-*`), so the target
names are the platform's `X-Webhook-*` convention — but dropping `X-Gitea-*`
breaks any receiver verifying signatures on it.

## Image / release lane

- Published as **`ghcr.io/hanzoai/git`** — v1-only, semver-pinned, never `:latest`.
  First release **`1.26.5`** (next patch over the upstream base 1.26.4).
- **The git tag IS the version.** `.hanzo/workflows/cicd.yml` (native CI, on the
  self-hosted `hanzo-build-linux-amd64` pool) delegates to
  `hanzoai/ci/.github/workflows/build.yml@v1`, which reads `hanzo.yml` and, on a
  `v*` ref, tags the image from the ref itself — `ghcr.io/hanzoai/git:v1.26.22`
  plus the v-stripped `1.26.22`. A branch push gets only `sha-<sha7>-amd64`. The
  Makefile derives `main.Version` from the same tag (`git describe` over the
  checkout), so the tag, the image tag and `gitd --version` are one fact.
- **Cut a release: `git tag -a v1.26.22 && git push canonical v1.26.22`.**
  `canonical` is `git.hanzo.ai/hanzoai/git`. Pushing a tag to the GitHub mirror
  builds NOTHING: `sync-from-github.yml` fast-forwards `main` and nothing else,
  by design — releases are declared where CI runs.
- Do **not** `crane copy` a `sha-` image onto a semver name. `v1.26.19` and
  `v1.26.20` were made that way while the tag lane was red, and both carry a
  binary that reports its commit instead of its version.

## Where it runs

Operator-managed in `hanzoai/universe` (DOKS `hanzo-k8s`, namespace `hanzo`):

- `infra/k8s/operator/crs/git.yaml` — the `hanzo-git` App (this image), SQLite on
  the RWO `gitea-data` PVC, OIDC via the `oauth-sync` init container. Synced by
  Hanzo CD (ArgoCD; the App-only successor to the operator GitSource).
- `infra/k8s/git/` — the App's non-App supporting resources (Hanzo CD's project is
  `hanzo.ai/App`-only): `gitea-data` PVC, `hanzo-git-oauth` ConfigMap, the
  `git.hanzo.ai` Ingress, and `git-secrets-kms.yaml` (gitea-secrets from KMS).
- `infra/k8s/git-runner/` — the act_runner DinD pool (upstream
  `gitea/act_runner:0.6.1-dind`) that runs Actions jobs; maps `hanzo-build-linux-amd64`.
- Push-to-deploy: a Gitea push webhook → cloud `/v1/git/webhook` → the `/v1/runner`
  build core. Architecture: `universe/docs/architecture/paas-in-cloud.md` §9.

The migration (fork becomes THE git server, replacing the raw upstream-image deploy
and the cloud embedded git seam as the host) is STAGED — the coordinator flips it.

## Cloud-native, multi-tenant, S3-aware: what actually blocks it

Measured 2026-07-26 against the running deploy, not inferred.

**Blobs are already on S3.** `GIT__storage__STORAGE_TYPE=s3` →
`s3.hanzo.svc:9000`, bucket `git`. Gitea's unified `[storage]` section covers
attachments, LFS, avatars, repo-archives, packages and Actions logs/artifacts, so
all of that is off the volume already. This is the part people assume is missing
and it is done.

**What still pins the server to one node** — the 250Gi RWO PVC holds 166G:

| Path | Size | Why it pins |
|---|---|---|
| `/data/gitea/data` | 145G | bare git repositories |
| `/data/gitea/indexers` | 18.4G | bleve code index |
| `/data/gitea/gitea.db` | 2.3G | **SQLite**, WAL mode |

That is why the Deployment is `replicas: 1` with `strategy: Recreate`, and why a
routine image bump caused a ~10 minute outage on 2026-07-26: the new pod hit
`Multi-Attach` on the RWO volume while terminated pods still held the
attachment, and the ReplicaSet wedged at zero. Single replica + RWO + Recreate
is not a tuning problem, it is the architecture.

### Ordering, cheapest unlock first

**1. SQLite → Postgres.** The single biggest unlock and the best
payoff-to-risk. A file-backed single-writer DB makes >1 replica impossible
*even if storage were shared*, so every later step is blocked behind this one.
Gitea supports Postgres natively with a documented dump/restore path. Blast
radius is the database alone; reversible from a dump. Nothing about repos,
hooks or S3 changes.

**2. bleve → external indexer, or off.** 18.4G of local index, and it is
*derived* — rebuildable from repos, so losing it costs time, not data. Set
`REPO_INDEXER_TYPE=elasticsearch` (or disable code search) and another local-file
dependency is gone. Low risk precisely because it is derived state.

After 1 and 2 the volume holds only repositories. That is the honest boundary.

**3. Repositories — the hard one, and S3 is not the answer.** git wants POSIX
semantics: rename-into-place, locking, mmap'd packfiles. An object store does not
provide them, so "put repos on S3" is not a configuration change; it is a storage
engine. Three real options, in ascending cost:

  - **RWX filesystem** (CephFS / an NFS service). N replicas share one tree, no
    code change, and `Recreate` becomes `RollingUpdate`. DO block storage is
    RWO-only, so this means running a filesystem service. Cheapest path to HA.
  - **Shard by tenant.** Each tenant gets its own instance and volume. No shared
    filesystem needed and it is genuinely multi-tenant, but it multiplies
    instances to operate and moves the problem to routing and provisioning.
  - **Object-backed git** (the Gitaly shape: a service owning repo storage that
    everything else calls). Correct end state, largest effort, and only worth it
    at a scale we are not at.

**4. Hooks are not the blocker.** Every push re-execs the binary as a git hook.
That looks unfashionable but it is node-local to whichever replica holds the
repo, so it neither blocks HA nor multi-tenancy. Leave it alone. Note the hooks
embed `setting.AppPath` — the runtime path, not a hardcoded name — which is why
the gitea→gitd rename regenerated every repo's hooks automatically. Do not
"improve" that into a constant.

### Multi-tenant means two different things — decide which

Gitea's model is one instance, one user table, orgs inside it.

  - **Orgs as tenants** — mostly already true; the work is isolation and quota,
    not architecture.
  - **Instance per tenant** — real isolation, and it is option 3b above.

These have almost nothing in common. Picking one is a prerequisite to the repo
storage decision, not a consequence of it.

### If you only do one thing

Step 1. Postgres removes the single-writer file that blocks every other step, and
it can ship on its own with a dump to roll back to.
