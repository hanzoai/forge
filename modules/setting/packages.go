// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"fmt"
	"math"
	"strings"

	"github.com/dustin/go-humanize"
)

// Package registry settings
var (
	Packages = struct {
		Storage *Storage
		Enabled bool

		// BaseURL is where package clients are told to fetch from. Package
		// metadata carries absolute URLs -- an npm tarball, a Cargo download,
		// a NuGet page -- and by default those name this server. Set it when
		// clients reach the registry through a different host than the web UI,
		// so the URLs handed out point at the host the client can actually
		// use. Expects the base that owner/ecosystem hangs off, e.g.
		// "https://api.example.com/v1/packages". Empty means "use my own URL".
		BaseURL string

		LimitTotalOwnerCount    int64
		LimitTotalOwnerSize     int64
		LimitSizeAlpine         int64
		LimitSizeArch           int64
		LimitSizeCargo          int64
		LimitSizeChef           int64
		LimitSizeComposer       int64
		LimitSizeConan          int64
		LimitSizeConda          int64
		LimitSizeContainer      int64
		LimitSizeCran           int64
		LimitSizeDebian         int64
		LimitSizeGeneric        int64
		LimitSizeGo             int64
		LimitSizeHelm           int64
		LimitSizeMaven          int64
		LimitSizeNpm            int64
		LimitSizeNuGet          int64
		LimitSizePub            int64
		LimitSizePyPI           int64
		LimitSizeRpm            int64
		LimitSizeRubyGems       int64
		LimitSizeSwift          int64
		LimitSizeTerraformState int64
		LimitSizeVagrant        int64

		DefaultRPMSignEnabled bool

		// GoProxyUpstream makes the Go registry a READ-THROUGH CACHE of the public
		// module ecosystem: on a miss the server fetches the module ONCE, stores
		// it as an ordinary package version, and serves every later request from
		// our own disk. ON by default, because a cache nobody remembered to
		// enable is just a slower registry.
		//
		// Set it empty to go back to publish-only.
		//
		// It caches, it does not gate: nothing is pinned, vetted or approved
		// here, so adding a dependency never needs permission and nobody has a
		// reason to route around the cache. Supply-chain PINNING is a different
		// concern and belongs in a different mechanism.
		//
		// The checksum database still verifies every module, so a cached copy
		// cannot become a place where a tampered module hides.
		GoProxyUpstream string

		// GoProxyPrivate is the real safety property, and it is why turning the
		// cache on is safe. A PRIVATE module is not published to the public
		// ecosystem, so a local miss on one would otherwise send its import path
		// -- github.com/hanzoai/<unreleased-thing> -- to proxy.golang.org, which
		// logs it. The path alone leaks a repository name that may not be public
		// yet.
		//
		// Any module whose path matches one of these comma-separated prefixes is
		// NEVER fetched upstream: a miss stays a miss. Same spirit as GOPRIVATE,
		// enforced server-side so it does not depend on every client being
		// configured correctly.
		GoProxyPrivate string

		// ConanUpstream makes the Conan registry a READ-THROUGH CACHE of the
		// public C++ ecosystem, in the one case where a Conan reference names
		// something immutable: the recipe files of
		// name/version@user/channel#recipe_revision.
		//
		// Anything vaguer -- "latest", a revision-less v1 URL -- is refused
		// rather than resolved here, because picking a revision on the caller's
		// behalf would pin whatever upstream published first into a name that
		// promises never to change. Binary packages are not cached; they are
		// selected by a settings hash computed against the caller's own
		// dependency graph.
		//
		// Set it empty to go back to publish-only.
		ConanUpstream string

		// ConanPrivate is what makes turning the cache on safe. A PRIVATE recipe
		// is not published publicly, so a local miss on one would otherwise send
		// its name -- lux-crypto, luxcpp -- to a public remote, which logs it.
		// The name alone leaks an internal library that may not be public yet.
		//
		// Any recipe whose name matches one of these comma-separated prefixes is
		// NEVER fetched upstream: a miss stays a miss. Enforced server-side so it
		// does not depend on every client's remote order being right.
		ConanPrivate string

		// CargoUpstream is the same read-through cache for Rust: on a miss the
		// server fetches the crate ONCE, stores it as an ordinary package
		// version, and serves every later request from our own disk. ON by
		// default, for the same reason as GoProxyUpstream -- a cache nobody
		// remembered to enable is just a slower registry.
		//
		// Set it empty to go back to publish-only.
		//
		// It must speak the crates.io web API: version metadata at
		// /api/v1/crates/{crate}/{version}, its dependencies at
		// .../dependencies, the file at .../download. Point it at an internal
		// mirror of that API and caching follows it.
		//
		// Every cached crate is checked against the SHA256 the upstream
		// publishes for it, so a cached copy cannot become a place where a
		// tampered crate hides.
		CargoUpstream string

		// CargoPrivate is the same never-leak guard as GoProxyPrivate: a crate
		// whose name starts with one of these comma-separated prefixes is NEVER
		// fetched upstream, because a miss on a private crate would otherwise
		// send its name to crates.io, which logs it.
		//
		// Empty by default, unlike GoProxyPrivate and ConanPrivate: a crate name
		// is FLAT -- there is no org in it -- so no prefix is knowably ours, and
		// any default we picked would be a guess that stops caching someone
		// else's public dependency. Deployments that publish private crates
		// under a naming convention ("hanzo-", "lux-") set it; deployments that
		// do not need nothing.
		CargoPrivate string
	}{
		Enabled:              true,
		LimitTotalOwnerCount: -1,
		GoProxyUpstream:      "https://proxy.golang.org",
		GoProxyPrivate:       "github.com/hanzoai/,github.com/luxfi/,github.com/zooai/,git.hanzo.ai/",
		ConanUpstream:        "https://center.conan.io",
		ConanPrivate:         "hanzo-,lux-,luxcpp,zoo-",
		CargoUpstream:        "https://crates.io",
	}
)

func loadPackagesFrom(rootCfg ConfigProvider) (err error) {
	sec, _ := rootCfg.GetSection("packages")
	if sec == nil {
		Packages.Storage, err = getStorage(rootCfg, "packages", "", nil)
		return err
	}

	if err = sec.MapTo(&Packages); err != nil {
		return fmt.Errorf("failed to map Packages settings: %v", err)
	}

	Packages.Storage, err = getStorage(rootCfg, "packages", "", sec)
	if err != nil {
		return err
	}

	Packages.LimitTotalOwnerSize = mustBytes(sec, "LIMIT_TOTAL_OWNER_SIZE")
	Packages.LimitSizeAlpine = mustBytes(sec, "LIMIT_SIZE_ALPINE")
	Packages.LimitSizeArch = mustBytes(sec, "LIMIT_SIZE_ARCH")
	Packages.LimitSizeCargo = mustBytes(sec, "LIMIT_SIZE_CARGO")
	Packages.LimitSizeChef = mustBytes(sec, "LIMIT_SIZE_CHEF")
	Packages.LimitSizeComposer = mustBytes(sec, "LIMIT_SIZE_COMPOSER")
	Packages.LimitSizeConan = mustBytes(sec, "LIMIT_SIZE_CONAN")
	Packages.LimitSizeConda = mustBytes(sec, "LIMIT_SIZE_CONDA")
	Packages.LimitSizeContainer = mustBytes(sec, "LIMIT_SIZE_CONTAINER")
	Packages.LimitSizeCran = mustBytes(sec, "LIMIT_SIZE_CRAN")
	Packages.LimitSizeDebian = mustBytes(sec, "LIMIT_SIZE_DEBIAN")
	Packages.LimitSizeGeneric = mustBytes(sec, "LIMIT_SIZE_GENERIC")
	Packages.LimitSizeGo = mustBytes(sec, "LIMIT_SIZE_GO")
	Packages.LimitSizeHelm = mustBytes(sec, "LIMIT_SIZE_HELM")
	Packages.LimitSizeMaven = mustBytes(sec, "LIMIT_SIZE_MAVEN")
	Packages.LimitSizeNpm = mustBytes(sec, "LIMIT_SIZE_NPM")
	Packages.LimitSizeNuGet = mustBytes(sec, "LIMIT_SIZE_NUGET")
	Packages.LimitSizePub = mustBytes(sec, "LIMIT_SIZE_PUB")
	Packages.LimitSizePyPI = mustBytes(sec, "LIMIT_SIZE_PYPI")
	Packages.LimitSizeRpm = mustBytes(sec, "LIMIT_SIZE_RPM")
	Packages.LimitSizeRubyGems = mustBytes(sec, "LIMIT_SIZE_RUBYGEMS")
	Packages.LimitSizeSwift = mustBytes(sec, "LIMIT_SIZE_SWIFT")
	Packages.LimitSizeTerraformState = mustBytes(sec, "LIMIT_SIZE_TERRAFORM_STATE")
	Packages.LimitSizeVagrant = mustBytes(sec, "LIMIT_SIZE_VAGRANT")
	Packages.DefaultRPMSignEnabled = sec.Key("DEFAULT_RPM_SIGN_ENABLED").MustBool(false)
	Packages.BaseURL = strings.TrimSuffix(sec.Key("BASE_URL").MustString(""), "/")
	return nil
}

// PackageRegistryURL is the base URL a client should use for owner's ecosystem
// registry. Every package handler builds its absolute URLs from this, so the
// host clients are pointed at is decided in exactly one place.
//
// Callers pass owner already escaped or lower-cased as their ecosystem
// requires; this only joins.
func PackageRegistryURL(owner, ecosystem string) string {
	if Packages.BaseURL != "" {
		return Packages.BaseURL + "/" + owner + "/" + ecosystem
	}
	return AppURL + "v1/packages/" + owner + "/" + ecosystem
}

func mustBytes(section ConfigSection, key string) int64 {
	const noLimit = "-1"

	value := section.Key(key).MustString(noLimit)
	if value == noLimit {
		return -1
	}
	bytes, err := humanize.ParseBytes(value)
	if err != nil || bytes > math.MaxInt64 {
		return -1
	}
	return int64(bytes)
}
