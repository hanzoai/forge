# Hanzo Git — marketing landing (git.hanzo.ai)

The product landing page served at the root of **git.hanzo.ai** after the cutover
from stock Gitea to native Hanzo Git. A self-contained static page (HTML + CSS +
inline SVG, zero JS, zero external assets) whose "Open dashboard" CTA links to
**console.hanzo.ai/git** — the Git dashboard lives in the console; this host is the
marketing page plus the `git clone` / `git push` transport.

## Files

- `index.html` — the page: Hanzo Git hero, one line on native hosting + CI + paas
  publish, the `git clone https://git.hanzo.ai/<org>/<repo>.git` code block, and the
  Login → console CTA. On-brand (Hanzo H mark), responsive, light + dark via
  `prefers-color-scheme`.
- `favicon.svg` — the Hanzo H mark (theme-aware).
- `robots.txt` — index only `/`; the rest of the host is the git transport.
- `Dockerfile` — serves the page via **hanzoai/static** (the canonical Hanzo static
  server, NOT nginx/caddy). Built by CI to `ghcr.io/hanzoai/git-site` — never on a
  dev box.

## Build (CI)

```
docker build -t ghcr.io/hanzoai/git-site:<tag> .   # CI only — multi-arch runners
```

`hanzoai/static` serves `/srv` on `:3000`. The page needs only inline styles + the
data:/self favicon, so the CSP stays tight (`default-src 'self'; style-src 'self'
'unsafe-inline'; img-src 'self' data:`), set via `HANZO_STATIC_CSP` on the deployment.

## Deploy

Two manifests in `universe/infra/k8s`:

- `operator/crs/git-site.yaml` — a `hanzo.ai/v1 kind: Service` CR that runs
  `ghcr.io/hanzoai/git-site` as `git-site.hanzo.svc:80` (ingress disabled — the git
  Ingress owns host routing).
- `git/ingress.yaml` — the two-Ingress split for `git.hanzo.ai`: `/` (+ favicon /
  robots) → `git-site:80` at high `router.priority`; everything else (git smart-HTTP,
  `/v1/git`, `/git` UI) → `cloud:8000` as the catch-all. See that file's header for
  why the priority split is load-bearing.

## White-label

This page is Hanzo-branded because git.hanzo.ai is a Hanzo host (the console's `git`
product is scoped `brands: ['hanzo']`). White-labeling is by DEPLOYMENT, not runtime
JS: a Lux/Zoo git host would build + deploy its own brand `git-site` image. One way,
no hostname sniffing on a static page.
