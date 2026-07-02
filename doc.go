// Package git is Hanzo's native Git code-hosting subsystem — an embeddable
// (NOT a Gitea-monolith) git layer that welds into the unified cloud binary via
// cloud.Register, the same way hanzoai/ai and hanzoai/base do.
//
// Design (decomplected, "one and only one way"):
//
//   - A REPO is the Git layer: source code, buildable/deployable. It lives UNDER
//     an IAM project (tenancy context) — it is NOT the project. Scope every repo
//     by the gateway-minted X-Org-Id (tenant) + optional X-Project-Id (sub-scope).
//   - OBJECTS (packs, refs, blobs) are stored by go-git on a go-billy filesystem
//     backed by hanzoai/vfs → S3/SeaweedFS. Every byte lives in OUR object store;
//     Hanzo is fully independent of GitHub.
//   - METADATA (repo name, default branch, size, project, timestamps) lives in
//     hanzoai/base (SQLite), IAM-native.
//   - SURFACE at /v1/git: a JSON repo control-plane + git smart-HTTP
//     (info/refs, git-upload-pack, git-receive-pack) so `git clone/push` work
//     natively against api.hanzo.ai/v1/git/<org>/<repo>.git.
//   - BILLING is first-class: every repo tracks sizeBytes and emits usage so
//     commerce meters storage; nothing is free.
//   - GitHub is an OPTIONAL OUTBOUND mirror (push our repos out for backup/public
//     visibility), never an inbound dependency — build/test/run/deploy/publish all
//     stay in-house.
//
// Gitea is the reference for the richer surface (issues/PRs/releases); we PORT
// its proven, go-git-based hosting patterns incrementally into this embeddable
// package rather than vendoring the standalone app.
package git
