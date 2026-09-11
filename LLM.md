# Hanzo Forge — `github.com/hanzoai/git`

The Git forge that serves `git.hanzo.ai`: IAM-native code hosting
for the Hanzo / Lux / Zoo orgs, with native GitHub-Actions-compatible CI.

## What it is

- **Base:** upstream **1.26.4**, credited in `NOTICE`. Module path is
  `github.com/hanzoai/git` — the GitHub repo rename to
  `hanzoai/forge` did not change it. The daemon is **`gitd`**
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
  `routers/api/actions` — full runner registration + job API. Enabled via
  `GIT__actions__ENABLED=true`.
- **The runner protocol is ours: five typed zip operations at `/v1/runner`** —
  `register`, `declare`, `task`, `state`, `log`, declared once with `zip.Post`
  and answered on two faces: `POST /v1/runner/<name>` for a runner over HTTP, and
  `post_runner_<name>` on ZAP's own call plane at `/.well-known/zip/op/`. Both
  are the same app on the forge's one listener — `routers/init.go` hangs it on
  two chi routes with `adaptor.FiberApp`, registered with `Post` and never
  `Mount`, because Mount strips the prefix and the operations declare absolute
  paths. It is spoken only between this forge and `hanzoai/git-runner`, so it is
  written to suit us: no protobuf, no generated code, no service name in the path.
  Two rules govern every value on it, and a test in `routers/api/actions` holds
  both: no `map`, which the ZAP layout refuses outright, and no `time.Time`,
  which it accepts and then silently blanks because every field of one is
  unexported. Named values cross as `[]Pair`; instants as int64 unix nanoseconds.
  `routers/api/actions/runner.go` holds the handlers; the messages live in
  `modules/actions/runner`, a NESTED MODULE
  (`github.com/hanzoai/git/modules/actions/runner`) with an empty `require`
  block. That nesting is the point: the runner imports the very types the
  handlers declare, so there is one definition of the protocol and it does NOT
  inherit this module's dependency graph. Requiring the whole forge instead
  breaks the runner outright — the forge pins `go.yaml.in/yaml/v4` forward with
  a `replace`, a dependent does not inherit a `replace`, and `actionlint` then
  fails to compile.
- **A runner's job context is a struct, and that is the point.** As a map the
  two sides could disagree in silence, and did: the forge wrote
  one spelling of the runtime-token key while the runner read another, and
  quietly fell back to the task token. `runner.Context` names the twenty-one values the
  forge computes AND the runner reads, so a missing one is a compile error.
  `GenerateGitContext` keeps its map — the forge evaluates workflow expressions
  against it — and `generateTaskContext` is the ONE place the two vocabularies
  meet.
- **The forge and the runner share that wire, so they are deployed together.**
  The forge serves one runner protocol and the runner speaks one; nothing
  negotiates a version and there is no shim, deliberately. So a protocol change
  lands in both repos or in neither, and a rollout goes forge first, runner
  fleet second: in the gap a runner's operations fail, it backs off, jobs queue
  here, and the queue drains once the fleet is rolled. The other order points
  runners at a forge that does not yet answer them.
- **Artifacts are third-party JavaScript and the paths are not ours.**
  `actions/upload-artifact@v3` concatenates `_apis/pipelines/…` onto whatever
  the runner set as `ACTIONS_RUNTIME_URL`, so that base IS ours and it sits at
  `/v1/artifact/`. The v4 protocol is different: `@actions/artifact` v2 reads
  `ACTIONS_RESULTS_URL` and keeps only `new URL(…).origin`, then appends
  `twirp/github.actions.results.api.v1.ArtifactService/…` itself — so
  `ArtifactV4RouteBase` cannot be moved anywhere, and it stays at the root.
  That same library throws `GHESNotSupportedError` for any host that is not
  `github.com`, `*.ghe.com` or `*.localhost`, so on `git.hanzo.ai` every
  `upload-artifact@v4+` / `download-artifact@v4+` step fails before it opens a
  connection: the v4 endpoints answer nobody today.
- **Identity = hanzo.id OIDC only.** No fork-baked issuer; binding is a standard
  OAuth2 auth source (goth `openidConnect`) pointed at
  `https://hanzo.id/.well-known/openid-configuration`. Org membership is driven by
  the IAM `owner` claim (`--group-claim-name owner --group-team-map …
  --group-team-map-removal`), reconciled declaratively by the deploy's `oauth-sync`
  init container. hanzo.id IAM app: see the universe repo for the registered name.
- **A Hanzo IAM access token IS a git credential.** `iamUser` (`services/auth/iam.go`),
  called from the basic-auth path (`services/auth/basic.go`), takes a hanzo.id access
  token as the git-over-HTTP password so CI and buildkit stop needing a hand-made PAT.
  Sign-in here is external, so a user has no password to give git and a PAT was a
  second credential with a second lifetime for an identity IAM already issues tokens
  for.
  - **Two wire shapes, one credential.** `basic.go` takes the token as the Basic
    PASSWORD, which is what git-over-https sends; `auth.IAM{}` (`iam.go`) takes it as a
    BEARER, which is what the API takes. That Method is registered in the API group and
    is NOT the same feature twice — `3dd8a290b8` added it to close the bearer gap this
    file used to list as owed. An earlier draft of this bullet said the Method "must not
    become one"; it was written before that gap was closed, and the commit supersedes it.
  - **No local credential is asked before it, because none is left.** The OAuth2 access
    token and the personal access token are gone from BOTH readers — `parseAuthBasic`
    and `OAuth2.userFromToken` — since IAM signs the JWT behind every access token and
    API key, and a credential this instance mints for itself is a second authority for
    an identity that already has one. Removing it from one reader only would have left
    the other answering: they are two paths to the same two credentials, which is why
    the change is not complete until both are cut.
  - **What those readers still accept is the Actions task credential**, in both its
    shapes — the JWT a current runner carries and the opaque token an older one sends.
    It is not a user identity and not IAM's to issue: the protocol mints it per job,
    scoped to that job, for the runner already executing it.
  - **Verification is the shared reader**, `hanzoai/authz`'s `edge.Verifier` over the
    issuer's JWKS — algorithm, kid, signature, issuer, expiry. Issuer and keys come from
    THIS instance's own OIDC login source via discovery, so the tokens accepted are the
    ones minted by the provider users already sign in through, and no `[iam]` config
    exists to drift from it. There is no bespoke verifier; building one is the custom
    auth the house rules forbid.
  - **`aud` is deliberately NOT checked.** IAM sets it to the client that ASKED for the
    token, not the server that accepts it, so an allowlist here would mean enumerating
    every client in the estate and 401ing every user of the next one. What bounds the
    credential instead is its SCOPE: `basic.go` sets `ApiTokenScope` to
    `write:repository`, so it clones, fetches and pushes and cannot mint tokens, add
    keys or administer anything.
  - **Whose token it is** comes from the link sign-in already wrote — `sub` against
    `external_login_user` for that source. No claim is trusted to NAME a user; email and
    username are mutable and a match on one would land a renamed identity on somebody
    else's account. An account that may not sign in is refused here too
    (`!IsIndividual || !IsActive || ProhibitLogin`), the same question db, ldap and
    signin each ask — without it one credential would outlive a suspension.
  - **The container routes stay out, deliberately.** `/v2/token` mints a 24h registry
    token of its own, which would outlive the short-lived IAM token and survive its
    revocation, so a longer-lived credential is not issued off a shorter-lived one.
- **Config = env.** `GIT__<section>__<KEY>` (upstream's app.ini API under our
  prefix; `modules/setting.EnvConfigKeyPrefixGit`). No other prefix is accepted, and
  there is no fallback, so a stale variable under an old prefix is ignored in
  silence. No
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

## The URL namespace

`routers.NormalRoutes` (`routers/init.go`) allocates the whole top level, and it
is the only place that may: chi matches the most specific mount first, so a
`/v1/…` route declared inside `routers/web` would be swallowed by the `/v1`
mount. Five prefixes:

- **`/` — the browser.** Every page a person sees (`routers/web`), plus
  `/-/fetch-redirect`, the delegate that lets a `fetch` response redirect to a
  URL carrying a hash.
- **`/v1` — every machine surface.** The REST API is mounted here whole
  (`routers/api/v1`). Beside that mount, not inside it, sit the endpoints that
  arrive with their own credential and so must not meet the session and
  API-token middleware: `/v1/internal` (the daemon calling itself from a git
  hook or the SSH command, holding the internal token), `/v1/sync` (HMAC-SHA256
  over the payload), `/v1/healthz` (no auth and no database, so a probe stays a
  probe), `/v1/packages`, `/v1/runner` and `/v1/artifact`.
- **`/.well-known/zip/op/` — ZAP's call plane**, the second face of the same five
  runner operations. It is a top-level path because that is where zip addresses
  an operation by name, and it rides the forge's existing listener.
- **`/v2` — the OCI distribution spec.** The registry API fixes this at the root
  of the host, so a sub-path deploy has to map it there in the proxy. Not ours to
  place.
- **`/twirp/github.actions.results.api.v1.ArtifactService`** — the v4 artifact
  protocol, also not ours to place, and answering nobody. See artifacts above.

**Nothing lives under `/api`.** The only thing that ever did was the runner
control plane, which is now `/v1/runner`. The host is `api.*` where an API
surface deserves its own name; the path carries `/v1/` and never a second
`/api/` segment as well.

## Upstream naming: what is left, and why

The tree names itself. Source files carry an SPDX line and the Hanzo copyright;
upstream authorship is recorded once in `NOTICE`. Everything addressable is
spelled for this product: webhook type `native`, storage bucket and container
`forge`, indexer names `forge_issues` / `forge_codes`, bleve analyzer
`forge/path`, lock prefix `forge:globallock:`, SSH host keys `ssh/forge.*`,
markdown front-matter key `forge:`, metrics namespace `git_`.

What remains is there for a reason. Do NOT sed these:

- **Attribution.** `LICENSE` and `NOTICE` carry the upstream copyright the MIT
  licence requires, and `assets/go-licenses.json` embeds each dependency's
  licence text verbatim — including `github.com/hanzoai/act`, whose own LICENSE
  still credits its upstream. Rewriting a licence text falsifies it.
- **`go.sum`.** `gitea.com/xorm/sqlfiddle` is in the module graph because
  `github.com/hanzoai/builder` keeps it as a test dependency. Dropping it there
  and releasing a new builder clears the last entry here.
- **Legacy on-disk names, which are load-bearing.**
  `modules/gitrepo/hooks.go` keeps `legacyDelegateHookNames` and
  `models/asymkey/ssh_key_authorized_keys.go` keeps the old authorized_keys
  marker. Both exist so state an earlier release wrote is *removed*: an
  unrecognised delegate exits 127 and rejects every push, and an unrecognised
  marker is copied through as a hand-added key, leaving a revoked key
  authorized. Forgetting these is a defect, not a cleanup.
- **`"gitea-actions"` in the reserved-username list** (`models/user/user.go`).
  Unblocking it would let an account squat the name the Actions bot used to
  have.
- **The reserved secret-name prefix** (`services/secrets/validation.go`) rejects
  `GITEA_` alongside `GITHUB_`. Nothing in this tree injects the former, but the
  runner is a separate component; narrow the denylist only after confirming it
  there.
- **`email_notification.gitea_actions`** (`models/user/setting_options.go`) is a
  stored `user_setting` key. The Go identifiers around it were renamed; the
  value needs a migration, so it waits for one.
- **`models/migrations`.** A migration must keep describing what it did: theme
  values, service-type enum values, the `io.gitea.commits` payload field it
  reads, and the fixture rows it ran against all stay. Comment text is fair game;
  nothing else is.
- **`tools/generate-svg.ts`** names the upstream icons it prunes, because the
  third-party icon theme is what ships them.

**Every header we emit is `X-Git-*`.** Webhook delivery
(`services/webhook/deliver.go`), notification mail (`services/mailer/`), and the
API response headers `X-Git-Warning` and `X-Git-Object-Type` (`routers/api/v1/`).
The vendor families beside them stay and carry the same values, because they are
what lets a receiver written for another server work against us unchanged:
`X-Gogs-*`, `X-GitHub-*`, `X-GitLab-*`, and `X-Hub-Signature`/`-256` — that last
pair is GitHub's own spelling, which third parties genuinely send us.
`TestWebhookDeliverGitHeaders` asserts the parity and refuses any delivery header
named after the upstream server, so the emission cannot come back by accident.

No inbound header is branded either. The OTP request header was declared in the
CORS allow-list and both swagger specs and read by nothing — identity here is
IAM's, and basic auth takes a token — so it and the test that asked for a
response the server could not produce are gone.

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
  an RWO data PVC, OIDC via the `oauth-sync` init container. Synced by
  Hanzo CD (ArgoCD; the App-only successor to the operator GitSource).
- `infra/k8s/git/` — the App's non-App supporting resources (Hanzo CD's project is
  `hanzo.ai/App`-only): the data PVC, `hanzo-git-oauth` ConfigMap, the
  `git.hanzo.ai` Ingress, and `git-secrets-kms.yaml` (secrets from KMS).
- `infra/k8s/git-runner/` — the DinD pool that runs Actions jobs
  (`statefulset.yaml`, image `oci.hanzo.ai/hanzoai/git-runner`); maps
  `hanzo-build-linux-amd64`. It rolls in the same change as this image, one
  after the other, because of the shared wire above.
- Push-to-deploy: a push webhook → cloud `/v1/git/webhook` → cloud's own
  `/v1/runner` build core — which is a different service on a different host that
  happens to share the path, not the runner surface described above.
  Architecture: `universe/docs/architecture/paas-in-cloud.md` §9.

The migration (fork becomes THE git server, replacing the raw upstream-image deploy
and the cloud embedded git seam as the host) is STAGED — the coordinator flips it.

## Credentials over https: the token a user already holds

Sign-in here is EXTERNAL (`ENABLE_PASSWORD_SIGNIN_FORM=false`,
`ALLOW_ONLY_EXTERNAL_REGISTRATION=true`), so a user has no password to hand git
over https and the only credential left used to be a personal access token they
had to go and make. `services/auth/iam.go` reads the one they already have: a
Hanzo IAM access token is accepted as the basic-auth password, so

```
git clone https://x:$(hanzo auth token)@git.hanzo.ai/<org>/<repo>.git
```

works from anywhere the CLI is signed in — a laptop, a CI job, a leased sandbox
(cloud's `apps/sandbox/cred.go` points a credential helper at it, which is what
makes git work in a shell with no browser).

- **Read once, by the platform's own verifier.** `hanzoai/authz` over the issuer's
  JWKS: algorithm, key id, signature, issuer, expiry. Issuer and keys come from
  THIS instance's OIDC login source, so what is accepted is what the provider
  users sign in through mints, and changing that provider changes this with it.
- **Resolved only through the link sign-in already wrote** — the token's `sub`
  against `external_login_user`. No claim NAMES a user; email and username are
  mutable and a match on one would land a renamed identity on another account. A
  subject that never signed in here resolves to nobody, and nothing is created
  from a credential: registration stays the web flow's, which is the only place
  that applies the whole policy (auto-registration, account linking, group and
  team mapping).
- **Scope is `write:repository`.** Clone, fetch, push. A credential that exists so
  a checkout works does not mint tokens, add keys, or administer anything.
- **Asked LAST**, after the OAuth2 token, the personal access token and the task
  token have each declined, and only for a credential shaped like a JWT — so a
  token this instance issued itself is never sent to a verifier that would reject
  it, and a PAT costs no network call.
- The AUDIENCE is not checked, matching `authz`'s documented position: IAM sets
  `aud` to the client that ASKED for the token, not to the server that may accept
  it.

## Cloud-native, multi-tenant: the house pattern, not Postgres

Measured 2026-07-26 against the running deploy. An earlier draft of this section
recommended SQLite -> Postgres. **That was wrong for this stack** and is removed:
Postgres buys HA for one shared schema, which is the opposite of what we want.
The house answer is SQLite per tenant, S3 as the source of truth, stateless
nodes.

### What is already true

Blobs are on S3 (`GIT__storage__STORAGE_TYPE=s3` -> `s3.hanzo.svc:9000`, bucket
`git`): attachments, LFS, avatars, archives, packages, Actions artifacts. Done.

What pins the server to one node is the 250Gi RWO PVC, 166G used:

| Path | Size | What it is |
|---|---|---|
| `<data>/data` | 145G | bare repositories |
| `<data>/indexers` | 18.4G | bleve index (derived, rebuildable) |
| `<data>/forge.db` | 2.3G | ONE SQLite for the whole instance |

That is why it is `replicas: 1` + `Recreate`, and why an image bump took
git.hanzo.ai down ~10 min on 2026-07-26 (new pod hit Multi-Attach while
terminated pods still held the volume; the ReplicaSet wedged at zero).

### The house stack this should use

- **`hanzoai/replicate`** — SQLite WAL replication to S3. One import, zero
  config files, zero sidecars; set `REPLICATE_S3_ENDPOINT` and it runs. Already
  wrapped as a Base plugin (`base/plugins/replicate`). luxfi/kms does the same
  shape with the ZapDB Replicator.
- **DB per tenant** — `hanzoai/commerce` already does this: `db.Manager` holds
  `userDBs map[string]*SQLiteDB` and `orgDBs map[string]*SQLiteDB`, opened on
  demand.

So the target for git is one SQLite per org/project, replicated to S3, with
local disk as a *cache* rather than the source of truth. Nodes become stateless:
any node can serve any tenant by materialising that tenant's DB from S3.

### Two gaps to close first — and they are shared, not git-specific

1. **No eviction.** commerce's `userDBs`/`orgDBs` maps are unbounded; handles are
   only closed by `Manager.Close()`. Open-per-tenant without eviction is a file
   descriptor and memory leak that grows with tenant count — exactly what makes a
   node stop being lightweight. Needs an LRU (or idle TTL) that closes cold
   handles and lets the local file be re-fetched on next use.
2. **Per-tenant DBs are not replicated.** commerce does NOT import
   `hanzoai/replicate` — its tenant DBs are local-only. So per-tenant SQLite
   exists and S3-backed SQLite exists, but nothing yet does both.

Close those two once, in the shared layer, and both commerce and git get it.

### Then: git embeds into cloud

`hanzoai/cloud` already owns the git control plane, smart-HTTP and SSH surface
(`clients/git/git.go` — `/v1/git/repos`, `/usage`, and root `/:org/:repo/*`
smart-HTTP for the git host). Today it *fronts* a separate stateful hanzo-git
Deployment; `cloud/go.mod` does not import `hanzoai/git`.

Embedding it natively is the actual "cloud-native" step, and it is the right one
because cloud is already horizontally scalable and stateless. Order matters:

1. eviction + replication in the shared tenant-DB layer (above),
2. git's schema split from one instance DB to per-org/project DBs,
3. cloud imports the fork as a module and serves git in-process,
4. the standalone hanzo-git Deployment and its RWO PVC go away.

Only step 4 removes the single-replica constraint, and it cannot come first.

### Repositories are still the hard part, and S3 is not the answer

git wants POSIX rename-into-place, locking and mmap'd packfiles; an object store
provides none of them, so "repos on S3" is a storage engine, not a config flag.
With per-tenant DBs the honest option is **shard by tenant** — a node owns a
tenant's repos while it owns that tenant's DB, which is the same sharding key,
and DO block volumes attach per node. Keep a KV in front for hot reads. An RWX
filesystem also works and needs no code change, but it means running a
filesystem service and it does not give tenancy — only HA.

### Do not "fix" this

Hooks re-exec the binary per push. That is node-local to whoever holds the repo,
so it blocks neither HA nor tenancy. The hook scripts embed `setting.AppPath`
(the runtime path), not a hardcoded name — which is why the binary rename
regenerated every repo's hooks by itself. Turning that into a constant would
break the next rename.

## Working here

Merged from AGENTS.md, which held these and nothing else. Two files meant two
places for a rule to live, and a rule that exists twice drifts.

- Use `make help` to find available development targets
- Run `make fmt` to format `.go` files, and run `make lint-go` to lint them
- Run `make lint-js` to lint `.ts` files
- Run `make tidy` after any `go.mod` changes
- Run single go tests with `go test -run '^TestName$' ./modulepath/`
- Run single js test files with `pnpm exec vitest <path-filter>`
- Run single playwright e2e test files with `GIT_TEST_E2E_FLAGS='<filepath>' make test-e2e`
- Add the current year into the copyright header of new `.go` files
- Ensure no trailing whitespace in edited files
- Use Conventional Commits for commit messages and PR titles, e.g. `type(scope): subject`; `!` before the colon if breaking. Use `test` type for test-only changes.
- Never force-push, amend, or squash unless asked. Use new commits and normal push for pull request updates
- Preserve existing code comments, do not remove or rewrite comments that are still relevant
- Keep comments short, prefer same-line, explain why, never narrate code
- Prefer unit tests over integration tests when logic is testable in isolation
- Aim for sub-2s local runtime for integration and e2e tests
- In TypeScript, use `!` (non-null assertion) instead of `?.`/`??` when a value is known to always exist
- For CSS layout, prefer `flex-*` helpers over per-child `tw-ml-*` / `tw-mr-*` margins; fall back to `tw-*` utilities when specificity requires `!important`
- Include authorship attribution in issue and pull request comments
- Always add `Assisted-By` trailers to commit messages in format `Assisted-by: AGENT_NAME:MODEL_VERSION`
- Never add `Co-Authored-By` `Signed-off-by` trailer to commit messages. Sign off must be done by a human.
