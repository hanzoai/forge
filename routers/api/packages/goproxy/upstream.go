// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package goproxy

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	packages_model "github.com/hanzoai/git/models/packages"

	packages_module "github.com/hanzoai/git/modules/packages"
	goproxy_module "github.com/hanzoai/git/modules/packages/goproxy"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/util"
	"github.com/hanzoai/git/services/context"
	packages_service "github.com/hanzoai/git/services/packages"
)

// Read-through caching for the public Go module ecosystem.
//
// Without this the registry serves ONLY what was published to it, so every CI
// run and every code-intelligence index re-fetches the same modules from
// proxy.golang.org. That is slow, it makes every build depend on someone else's
// uptime, and a module yanked or retagged upstream silently changes what we
// build.
//
// With `[packages] GOPROXY_UPSTREAM` set, a miss fetches the module ONCE and
// stores it as an ordinary package version — the same rows, files and quota the
// publish path creates, so there is exactly one storage shape and `UploadPackage`
// stays the only writer of Go packages. Everything downstream (listing, quota,
// cleanup, backup) works on cached modules for free, because they are not a
// special case.
//
// Fail-soft by construction: if upstream is unreachable, slow or 404s, the
// handler falls back to the ordinary not-found it would have returned anyway. A
// cache that is down degrades to the behaviour of having no cache — it never
// turns a working registry into a broken one.

const upstreamTimeout = 60 * time.Second

// upstreamEnabled reports whether read-through caching is configured. Empty
// means publish-only, which is what every deployment has until someone opts in.
func upstreamEnabled() bool {
	return strings.TrimSpace(setting.Packages.GoProxyUpstream) != ""
}

// upstreamFetch GETs one file from the configured module proxy. `elem` is the
// GOPROXY-relative path, e.g. "example.com/mod/@v/v1.2.3.zip".
//
// The module path is lowercase-escaped per the GOPROXY spec (a capital letter
// becomes "!" + lowercase) — skipping that is the classic bug that makes
// github.com/BurntSushi/toml resolve for some clients and 404 for others.
func upstreamFetch(elem string) (io.ReadCloser, int64, error) {
	base := strings.TrimRight(strings.TrimSpace(setting.Packages.GoProxyUpstream), "/")
	req, err := http.NewRequest(http.MethodGet, base+"/"+elem, nil)
	if err != nil {
		return nil, 0, err
	}
	client := &http.Client{Timeout: upstreamTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("upstream %s: %s", elem, resp.Status)
	}
	return resp.Body, resp.ContentLength, nil
}

// escapeModule applies the GOPROXY case-encoding to a module path.
func escapeModule(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// cacheFromUpstream fetches one module@version and stores it exactly as
// UploadPackage would. It returns the stored version so the caller can serve
// the request it was already handling.
//
// Concurrency: two requests for the same missing module race, and the loser
// gets ErrDuplicatePackageVersion. That is a SUCCESS, not a failure — the module
// is in the cache either way — so it resolves rather than erroring.
func cacheFromUpstream(ctx *context.Context, name, version string) (*packages_model.PackageVersion, error) {
	if !upstreamEnabled() {
		return nil, packages_model.ErrPackageNotExist
	}
	// "latest" is a resolution question, not a fetchable artifact; only a
	// concrete version can be cached.
	if version == "" || version == "latest" {
		return nil, packages_model.ErrPackageNotExist
	}

	esc := escapeModule(name)
	body, _, err := upstreamFetch(fmt.Sprintf("%s/@v/%s.zip", esc, url.PathEscape(version)))
	if err != nil {
		return nil, err
	}
	defer body.Close()

	buf, err := packages_module.CreateHashedBufferFromReader(body)
	if err != nil {
		return nil, err
	}
	defer buf.Close()

	// Parse with the SAME parser the publish path uses, so a module that would
	// be rejected on upload is rejected here too. The cache can never hold
	// something the registry would refuse.
	pck, err := goproxy_module.ParsePackage(buf, buf.Size())
	if err != nil {
		return nil, err
	}
	if _, err := buf.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// Creator: the requesting user when there is one, else the owner of the
	// registry being read. An anonymous read must still be able to warm the
	// cache — a cache that only fills for logged-in callers would miss most CI.
	creator := ctx.Doer
	if creator == nil {
		creator = ctx.Package.Owner
	}

	_, _, err = packages_service.CreatePackageAndAddFile(
		ctx,
		&packages_service.PackageCreationInfo{
			PackageInfo: packages_service.PackageInfo{
				Owner:       ctx.Package.Owner,
				PackageType: packages_model.TypeGo,
				Name:        pck.Name,
				Version:     pck.Version,
			},
			Creator: creator,
			VersionProperties: map[string]string{
				goproxy_module.PropertyGoMod: pck.GoMod,
			},
		},
		&packages_service.PackageFileCreationInfo{
			PackageFileInfo: packages_service.PackageFileInfo{
				Filename: fmt.Sprintf("%v.zip", pck.Version),
			},
			Creator: creator,
			Data:    buf,
			IsLead:  true,
		},
	)
	if err != nil && !errors.Is(err, packages_model.ErrDuplicatePackageVersion) {
		return nil, err
	}

	return resolvePackage(ctx, ctx.Package.Owner.ID, name, version)
}

// resolveOrCache is the ONE seam every read endpoint goes through: serve what we
// have, otherwise fetch it once and serve that. Callers keep their existing
// not-found handling — an upstream that is disabled, unreachable or 404s simply
// yields the original miss.
func resolveOrCache(ctx *context.Context, name, version string) (*packages_model.PackageVersion, error) {
	pv, err := resolvePackage(ctx, ctx.Package.Owner.ID, name, version)
	if err == nil {
		return pv, nil
	}
	if !errors.Is(err, packages_model.ErrPackageNotExist) && !errors.Is(err, util.ErrNotExist) {
		return nil, err
	}
	if cached, cerr := cacheFromUpstream(ctx, name, version); cerr == nil {
		return cached, nil
	}
	return nil, err
}
