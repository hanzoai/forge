# hanzoai/git

Hanzo's **native Git code-hosting** — an embeddable git layer that welds into the
unified `cloud` binary (via the `subsystems.Wire()` slice, like `hanzoai/ai`). Lean
by design: go-git core on a local per-org volume for the live git objects +
`hanzoai/vfs`→S3 for LFS/attachments/artifacts + `hanzoai/base` (SQLite) for
metadata.

## Why

Hanzo keeps house: **build, test, run, deploy, publish — all inside our own
cloud.** `registry.hanzo.ai` replaced GHCR, ARC runners replaced GitHub-hosted
runners, `platform.hanzo.ai` deploys. `hanzoai/git` is the last piece — owning
**source** too. GitHub becomes an optional *outbound mirror*, never a dependency.

## Model

```
IAM   org → project → environment        (tenancy CONTEXT; billing + grouping)
git   repo (under a project)             (SOURCE: go-git on local per-org volume)
paas  deploy → S3 static / SSR runtime   (RUN: Sites & Deployments, metered)
```

A **repo** is scoped by the gateway-minted `X-Org-Id` (+ optional `X-Project-Id`).

## Surface — `/v1/git`

Control-plane (JSON): `POST /repos`, `GET /repos`, `GET /repos/:name`,
`DELETE /repos/:name`, `GET /usage`.
Smart-HTTP (native `git clone/push`): `GET /:org/:repo/info/refs`,
`POST /:org/:repo/git-upload-pack`, `POST /:org/:repo/git-receive-pack`.

Auth: IAM bearer (an `hk-` key from `POST /v1/iam/mint-user-keys`, or a
short-lived JWT from `POST /v1/iam/issue-user-token`).

## Billing

Every repo tracks `sizeBytes`; `/v1/git/usage` returns per-repo + total for the
tenant. Storage/bandwidth are metered to `hanzoai/commerce` — nothing is free.

## Build

Welds into `cloud`: `cloud`'s `subsystems/subsystems.go` lists it in `Wire()` as
`MountSpec{Name:"git", Mount: cloud.Typed(git.Mount)}`. One exported `Mount` —
no blank-import, no `init()` registry. Standalone: `go test ./...`.
