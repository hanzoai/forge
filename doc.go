// Package git is Hanzo's native Git code-hosting subsystem — a lean, embeddable
// git layer that welds into the unified cloud binary via
// the subsystems.Wire() slice (MountSpec{Name:"git", Mount: cloud.Typed(git.Mount)}),
// the same way hanzoai/ai and hanzoai/base do. There is ONE exported entrypoint,
// func Mount(*zip.App, cloud.Deps) error — no init() self-registration, no
// blank-import (cloud.Register is retired).
//
// Design (decomplected, "one and only one way"):
//
//   - A REPO is the Git layer: source code, buildable/deployable. It lives UNDER
//     an IAM project (tenancy context) — it is NOT the project. Scope every repo
//     by the gateway-minted X-Org-Id (tenant) + optional X-Project-Id (sub-scope).
//   - GIT OBJECTS (packs, refs, loose blobs) live in a bare repo on a persistent
//     LOCAL per-org volume at {DataDir}/git/orgs/<org>/<repo>.git — go-git's
//     dotgit needs real random-access POSIX semantics that vfs→S3 cannot give.
//     The object store is ours (no GitHub), just not S3 for the live git path.
//   - BLOBS + DR (LFS, attachments, avatars, Actions artifacts, run logs) stream
//     to hanzoai/vfs → S3/SeaweedFS; vfs replica Snapshot/Restore backs up the
//     per-org repo volume. VFS is the durable byte plane, not the hot git path.
//   - METADATA (repo name, default branch, size, project, timestamps) lives in
//     hanzoai/base (SQLite, one DB per org), IAM-native.
//   - SURFACE at /v1/git: a JSON repo control-plane + git smart-HTTP
//     (info/refs, git-upload-pack, git-receive-pack) so `git clone/push` work
//     natively against api.hanzo.ai/v1/git/<org>/<repo>.git.
//   - BILLING is first-class: every repo tracks sizeBytes and emits usage so
//     commerce meters storage; nothing is free.
//   - GitHub is an OPTIONAL OUTBOUND mirror (push our repos out for backup/public
//     visibility), never an inbound dependency — build/test/run/deploy/publish all
//     stay in-house.
//
// The richer surface (issues/PRs/releases + Actions CI) is built natively on
// hanzoai/orm and grown incrementally into this embeddable package.
package git
