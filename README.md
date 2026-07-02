# hanzoai/git

Hanzo's **native Git code-hosting** — an embeddable git layer that welds into the
unified `cloud` binary (`cloud.Register`, like `hanzoai/ai`). Not a Gitea
monolith: go-git core + `hanzoai/vfs`→S3 for objects + `hanzoai/base` (SQLite)
for metadata.

## Why

Hanzo keeps house: **build, test, run, deploy, publish — all inside our own
cloud.** `registry.hanzo.ai` replaced GHCR, ARC runners replaced GitHub-hosted
runners, `platform.hanzo.ai` deploys. `hanzoai/git` is the last piece — owning
**source** too. GitHub becomes an optional *outbound mirror*, never a dependency.

## Model

```
IAM   org → project → environment        (tenancy CONTEXT; billing + grouping)
git   repo (under a project)             (SOURCE: go-git objects in VFS→S3)
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

Welds into `cloud`: `cloud`'s `subsystems/subsystems.go` blank-imports
`github.com/hanzoai/git`; its `init()` calls `cloud.Register("git", …)`.
Standalone: `go test ./...`.
