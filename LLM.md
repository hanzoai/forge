# hanzoai/git — deep doc

Native, embeddable Git hosting welded into the Hanzo `cloud` binary.

## One-and-only-one-way

- **Repo ≠ project.** `project` is IAM's tenancy context (org→project→env, billing).
  A `repo` is the Git *source* layer, living under a project. Never conflate them
  (the collision that made `clients/projectsvc` call a buildable site a "project").
- **Objects in VFS→S3.** go-git writes to a go-billy FS backed by `hanzoai/vfs`.
  No local disk of record, no GitHub dependency — every byte in our object store.
- **Metadata in Base.** `hanzoai/base` (SQLite) holds repo rows (name, project,
  default branch, sizeBytes, timestamps). IAM-native.
- **Deploy is paas.** A repo builds → `hanzoai/paas` (the one deploy path) → S3
  static (cheap) or SSR runtime (metered compute). That lives in the Sites layer,
  not here — `git` owns SOURCE, not RUN.

## Mount contract (mirrors hanzoai/ai)

```go
func Mount(app *zip.App, deps cloud.Deps) error { ... }
func init() { cloud.Register("git", <order>, func(app any, deps cloud.Deps) error { ... }) }
```

`cloud/subsystems/subsystems.go` blank-imports `github.com/hanzoai/git`.

## Auth & tenancy (HIP-0111)

Gateway validates the IAM bearer and injects `X-Org-Id`/`X-User-Id`
(+ optional `X-Project-Id`); this package reads them, never trusts a client copy.
Agents authenticate with an `hk-` key (`/v1/iam/mint-user-keys`) — one credential
for `/v1/git` and `api.hanzo.ai/v1`, revocable, billable to its owner.

## Roadmap

MVP: repo CRUD + smart-HTTP (clone/push) over go-git+VFS + usage metering.
Next: build-on-push → paas deploy; issues/PRs/releases (port Gitea patterns);
optional outbound GitHub mirror.
