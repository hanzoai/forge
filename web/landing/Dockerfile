# Hanzo Git marketing landing — served by hanzoai/static (the canonical Hanzo
# static-hosting binary: a Go server on scratch, NOT nginx/caddy). The same image
# every first-party Hanzo marketing site uses (cloud-site, gallery). Built by CI to
# ghcr.io/hanzoai/git-site — NEVER on a dev box.
#
# The site is pure HTML + CSS + inline SVG (zero JS, zero external assets), so the
# default tight CSP holds with only inline styles allowed (set via HANZO_STATIC_CSP
# on the deployment — see universe/infra/k8s/operator/crs/git-site.yaml).
FROM ghcr.io/hanzoai/static:0.4.1

# The static root the server serves from (matches --root below).
COPY . /srv

# hanzoai/static listens on :3000; --spa is NOT set — this is a single real page,
# so an unknown path should 404 (it is the git smart-HTTP surface, routed to the
# cloud binary at the edge, never to this container).
ENTRYPOINT ["/static"]
CMD ["--root=/srv", "--port=3000"]
