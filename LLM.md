# hanzoai/git — deep doc

Native, embeddable Git hosting welded into the Hanzo `cloud` binary.

## One-and-only-one-way

- **Repo ≠ project.** `project` is IAM's tenancy context (org→project→env, billing).
  A `repo` is the Git *source* layer, living under a project. Never conflate them
  (the collision that made `clients/projectsvc` call a buildable site a "project").
- **Git objects on a local per-org volume.** Bare repos live at
  `{DataDir}/git/orgs/<org>/<repo>.git` on a persistent POSIX volume — go-git's
  dotgit needs real random-access FS semantics (`Rename`/`Lock`/`Truncate`), which
  `vfs→S3` does not provide. No GitHub dependency; the object store is ours, just
  not S3 for the *live* git path.
- **Blobs + DR in VFS→S3.** LFS objects, attachments, avatars, Actions artifacts,
  and run logs stream to `hanzoai/vfs`→S3; `vfs replica` Snapshot/Restore backs up
  the per-org repo volume for disaster recovery. VFS is the durable byte plane, not
  the hot git path.
- **Metadata in Base.** `hanzoai/base` (SQLite, one DB per org) holds repo rows
  (name, project, default branch, sizeBytes, timestamps). IAM-native.
- **Deploy is paas.** A repo builds → `hanzoai/paas` (the one deploy path) → S3
  static (cheap) or SSR runtime (metered compute). That lives in the Sites layer,
  not here — `git` owns SOURCE, not RUN.

## Mount contract (mirrors every cloud subsystem)

ONE exported entrypoint, wired explicitly — no `init()` self-registration, no
blank-import (`cloud.Register` is retired):

```go
func Mount(app *zip.App, deps cloud.Deps) error { ... }
```

`cloud/subsystems/subsystems.go` lists it in the positional `Wire()` slice:

```go
subsystems.Wire(
    ...,
    MountSpec{Name: "git", Mount: cloud.Typed(git.Mount)},  // + optional Shutdown / OwnsHealth
    ...,
)
```

`MountAll` gates on `cfg.Enabled("git")` and registers any `Shutdown` as a LIFO
`OnShutdown` hook. Today that slot resolves to `cloud/clients/git` (the ~60–70%
hosting core: repo CRUD, smart-HTTP + SSH clone/push, mirror, per-org isolation,
usage metering) — the seed we **promote** into this module, not rebuild.

## Auth & tenancy (HIP-0111)

Gateway validates the IAM bearer and injects `X-Org-Id`/`X-User-Id`
(+ optional `X-Project-Id`); this package reads them, never trusts a client copy.
Agents authenticate with an `hk-` key (`/v1/iam/mint-user-keys`) — one credential
for `/v1/git` and `api.hanzo.ai/v1`, revocable, billable to its owner.

## Roadmap

MVP: repo CRUD + smart-HTTP (clone/push) over go-git on the local per-org
volume + usage metering (promote `cloud/clients/git`).
Next: build-on-push → paas deploy; issues/PRs/releases + Actions server (native,
on `hanzoai/orm`); optional outbound GitHub mirror.
Full phased blueprint: `docs/PORT_PLAN.md`.
