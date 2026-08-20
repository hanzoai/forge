# syntax=docker/dockerfile:1
# Build frontend on the native platform to avoid QEMU-related issues with nodejs ecosystem
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26.5-alpine3.24 AS frontend-build
# go.mod pins the toolchain. The golang base image sets GOTOOLCHAIN=local,
# which turns a `go` directive newer than the image into a hard build
# failure instead of a download.
ENV GOTOOLCHAIN=auto
RUN apk --no-cache add build-base git nodejs pnpm
WORKDIR /src
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY --exclude=.git/ . .
RUN make frontend

# Build backend for each target platform
FROM docker.io/library/golang:1.26.5-alpine3.24 AS build-env
# go.mod pins the toolchain. The golang base image sets GOTOOLCHAIN=local,
# which turns a `go` directive newer than the image into a hard build
# failure instead of a download.
ENV GOTOOLCHAIN=auto

ARG GIT_VERSION
ARG TAGS=""
ENV TAGS="bindata timetzdata $TAGS"
ARG CGO_EXTRA_CFLAGS

# Build deps
RUN apk --no-cache add \
    build-base \
    git

WORKDIR ${GOPATH}/src/hanzo-git
# Exactly ONE hanzoai module in this graph is private: github.com/hanzoai/act.
# The other seven (xorm, builder, orm, sqlite, vfs, csqlite, sqlcipher) are public
# and resolve anonymously. GOPRIVATE marks the whole prefix so act takes a direct
# VCS fetch past the proxy and sumdb, and the gh_token rule below makes that fetch
# authenticated. No-op without the token, so public-only builds still work.
# gh_token is the secret id the canonical hanzoai/ci reusable mounts (was
# GIT_AUTH_TOKEN, which ci never set).
#
# act answers at github.com/hanzoai/act only through a RENAME REDIRECT: the repo
# now lives at github.com/hanzo-inc/act and still declares `module
# github.com/hanzoai/act`, so the module path is right and go is happy — provided
# the fetch can see the target. GitHub serves that 301 ONLY to an identity with
# read on hanzo-inc/act and a bare 404 to everyone else, so a build credential
# without it dies right here on "Repository not found" — which reads as a deleted
# or misspelled module and is neither. The fleet identity in KMS GITHUB_TOKEN
# (hanzo-dev) holds that read; if this line ever fails again, check that ACL
# before you touch go.mod.
ENV GOPRIVATE=github.com/hanzoai/*
COPY go.mod go.sum ./
RUN --mount=type=secret,id=gh_token \
    if [ -s /run/secrets/gh_token ]; then \
      export GIT_CONFIG_COUNT=1 \
             GIT_CONFIG_KEY_0="url.https://x-access-token:$(cat /run/secrets/gh_token)@github.com/.insteadOf" \
             GIT_CONFIG_VALUE_0="https://github.com/"; \
    fi; \
    go mod download
# Use COPY instead of bind mount as read-only one breaks makefile state tracking and read-write one needs binary to be moved as it's discarded.
# ".git" directory is mounted separately later only for version data extraction.
COPY --exclude=.git/ . .
COPY --from=frontend-build /src/public/assets public/assets

# Build gitd. The version comes from the GIT_VERSION arg, written to the VERSION
# file the Makefile prefers over `git describe`. The native runner fabric exports
# a tree rather than a clone, and the earlier bind on .git failed there with
# "/.git: not found"; line 39 excludes .git from the COPY as well, so inside this
# image the arg is the ONLY source of a version — a build that omits it gets
# 0.0.0+unknown whether or not it started from a clone. hanzoai/cloud passes it
# from the image tag (clients/platform buildFrontendCmd), which is why a tagged
# build is versioned and an ad-hoc `docker build` is not.
RUN --mount=type=cache,target="/root/.cache/go-build" \
    if [ -n "${GIT_VERSION}" ]; then echo "${GIT_VERSION}" > VERSION; \
    elif [ ! -d .git ] && [ ! -f VERSION ]; then echo "0.0.0+unknown" > VERSION; fi && \
    make backend

COPY docker/root /tmp/local

# Set permissions for builds that made under windows which strips the executable bit from file
RUN chmod 755 /tmp/local/usr/bin/entrypoint \
              /tmp/local/usr/local/bin/* \
              /tmp/local/etc/s6/gitd/* \
              /tmp/local/etc/s6/openssh/* \
              /tmp/local/etc/s6/.s6-svscan/* \
              /go/src/hanzo-git/gitd

FROM docker.io/library/alpine:3.24 AS hanzo-git

EXPOSE 22 3000

RUN apk --no-cache add \
    bash \
    ca-certificates \
    curl \
    gettext \
    git \
    linux-pam \
    openssh \
    s6 \
    sqlite \
    su-exec \
    gnupg

RUN addgroup \
    -S -g 1000 \
    git && \
  adduser \
    -S -H -D \
    -h /data/git \
    -s /bin/bash \
    -u 1000 \
    -G git \
    git && \
  echo "git:*" | chpasswd -e

COPY --from=build-env /tmp/local /
COPY --from=build-env /go/src/hanzo-git/gitd /app/git/gitd

ENV USER=git
ENV GIT_CUSTOM=/data/git

VOLUME ["/data"]

# HINT: HEALTH-CHECK-ENDPOINT: don't use HEALTHCHECK, search this hint keyword for more information
ENTRYPOINT ["/usr/bin/entrypoint"]
CMD ["/usr/bin/s6-svscan", "/etc/s6"]
