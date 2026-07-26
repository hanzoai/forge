// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package cargo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	packages_model "github.com/hanzoai/git/models/packages"

	"github.com/hanzoai/git/modules/json"
	cargo_module "github.com/hanzoai/git/modules/packages/cargo"
	"github.com/hanzoai/git/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shape and field names copied from a live crates.io response, so a drift in
// what we decode shows up here rather than as silently empty metadata.
const (
	upstreamVersionJSON = `{"version":{"id":499768,"crate":"rand","num":"0.8.5",
		"dl_path":"/api/v1/crates/rand/0.8.5/download","yanked":false,"yank_message":null,
		"lib_links":null,"license":"MIT OR Apache-2.0","crate_size":87113,
		"checksum":"%s","rust_version":null,"edition":"2018",
		"features":{"default":["std","std_rng"],"std":["rand_core/std"]},
		"description":"Random number generators and other randomness functionality.",
		"homepage":"https://rust-random.github.io/book",
		"documentation":"https://docs.rs/rand",
		"repository":"https://github.com/rust-random/rand"}}`

	upstreamDependenciesJSON = `{"dependencies":[
		{"id":3548105,"version_id":499768,"crate_id":"rand_core","req":"^0.6.0","optional":false,
		 "default_features":true,"features":[],"target":null,"kind":"normal","downloads":0},
		{"id":3548107,"version_id":499768,"crate_id":"bincode","req":"^1.2.1","optional":true,
		 "default_features":false,"features":["derive"],"target":null,"kind":"dev","downloads":0}]}`
)

// upstreamTestServer serves the two metadata documents plus a .crate whose
// SHA256 is the one the metadata advertises, and records every path it saw.
func upstreamTestServer(t *testing.T, crate []byte) (*httptest.Server, *[]string) {
	t.Helper()

	sum := sha256.Sum256(crate)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		// crates.io answers 403 without a User-Agent, so a missing one must fail
		// here too rather than only in production.
		if r.UserAgent() == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/v1/crates/rand/0.8.5":
			fmt.Fprintf(w, upstreamVersionJSON, hex.EncodeToString(sum[:]))
		case "/api/v1/crates/rand/0.8.5/dependencies":
			io.WriteString(w, upstreamDependenciesJSON)
		case "/api/v1/crates/rand/0.8.5/download":
			w.Write(crate)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, &seen
}

func TestUpstreamEnabled(t *testing.T) {
	defer func(v string) { setting.Packages.CargoUpstream = v }(setting.Packages.CargoUpstream)

	// Empty means publish-only — the behaviour a deployment gets by opting out.
	// Whitespace must not accidentally enable it.
	setting.Packages.CargoUpstream = ""
	assert.False(t, upstreamEnabled())
	setting.Packages.CargoUpstream = "   "
	assert.False(t, upstreamEnabled())

	setting.Packages.CargoUpstream = "https://crates.io"
	assert.True(t, upstreamEnabled())
}

func TestIsPrivateCrate(t *testing.T) {
	defer func(v string) { setting.Packages.CargoPrivate = v }(setting.Packages.CargoPrivate)
	setting.Packages.CargoPrivate = "hanzo-,lux-,zoo-"

	// Private names must never reach a public registry -- the name alone leaks a
	// library that may not be public yet.
	assert.True(t, isPrivateCrate("hanzo-node"))
	assert.True(t, isPrivateCrate("lux-consensus"))
	// crates.io treats case and -/_ as the same crate, so neither is a bypass.
	assert.True(t, isPrivateCrate("Hanzo-Node"))
	assert.True(t, isPrivateCrate("hanzo_node"))

	// Public crates still cache.
	assert.False(t, isPrivateCrate("serde"))
	assert.False(t, isPrivateCrate("rand"))
	// Near-misses must NOT be blocked: prefixes end at the separator, and a
	// private prefix in the middle of a name is somebody else's crate.
	assert.False(t, isPrivateCrate("hanzocore"))
	assert.False(t, isPrivateCrate("hanzo"))
	assert.False(t, isPrivateCrate("my-hanzo-node"))
	assert.False(t, isPrivateCrate("luxury"))

	// Empty config blocks nothing -- the default.
	setting.Packages.CargoPrivate = ""
	assert.False(t, isPrivateCrate("hanzo-node"))
}

func TestUpstreamGet(t *testing.T) {
	defer func(v string) { setting.Packages.CargoUpstream = v }(setting.Packages.CargoUpstream)

	srv, seen := upstreamTestServer(t, []byte("CRATEBYTES"))

	// A trailing slash on the configured base must not produce a doubled slash.
	setting.Packages.CargoUpstream = srv.URL + "/"
	body, err := upstreamGet("api/v1/crates/rand/0.8.5/download")
	require.NoError(t, err)
	defer body.Close()
	assert.Equal(t, []string{"/api/v1/crates/rand/0.8.5/download"}, *seen)

	content, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, "CRATEBYTES", string(content))

	// A non-200 is an error, never an empty body silently cached as a crate.
	setting.Packages.CargoUpstream = srv.URL
	_, err = upstreamGet("api/v1/crates/rand/9.9.9/download")
	require.Error(t, err)
}

func TestFetchUpstreamMetadata(t *testing.T) {
	defer func(v string) { setting.Packages.CargoUpstream = v }(setting.Packages.CargoUpstream)

	crate := []byte("CRATEBYTES")
	srv, _ := upstreamTestServer(t, crate)
	setting.Packages.CargoUpstream = srv.URL

	uc, err := fetchUpstreamMetadata("rand", "0.8.5")
	require.NoError(t, err)

	sum := sha256.Sum256(crate)
	assert.Equal(t, hex.EncodeToString(sum[:]), uc.checksum)
	assert.False(t, uc.yanked)

	assert.Equal(t, "rand", uc.meta.Name)
	assert.Equal(t, "0.8.5", uc.meta.Vers)
	assert.Equal(t, "MIT OR Apache-2.0", uc.meta.License)
	assert.Equal(t, "Random number generators and other randomness functionality.", uc.meta.Description)
	assert.Equal(t, "https://docs.rs/rand", uc.meta.Documentation)
	assert.Equal(t, "https://rust-random.github.io/book", uc.meta.Homepage)
	assert.Equal(t, "https://github.com/rust-random/rand", uc.meta.Repository)
	assert.Empty(t, uc.meta.Links) // `lib_links: null` is no links, not "null"
	assert.Equal(t, map[string][]string{"default": {"std", "std_rng"}, "std": {"rand_core/std"}}, uc.meta.Features)

	// Dependencies come from the second document; the API's field names are
	// translated to the publish format's.
	require.Len(t, uc.meta.Deps, 2)
	assert.Equal(t, publishDependency{
		Name: "rand_core", VersionReq: "^0.6.0", Features: []string{},
		Optional: false, DefaultFeatures: true, Kind: "normal",
	}, uc.meta.Deps[0])
	assert.Equal(t, publishDependency{
		Name: "bincode", VersionReq: "^1.2.1", Features: []string{"derive"},
		Optional: true, DefaultFeatures: false, Kind: "dev",
	}, uc.meta.Deps[1])
}

func TestUpstreamWireFormatRoundTrip(t *testing.T) {
	defer func(v string) { setting.Packages.CargoUpstream = v }(setting.Packages.CargoUpstream)

	crate := []byte("CRATEBYTES")
	srv, _ := upstreamTestServer(t, crate)
	setting.Packages.CargoUpstream = srv.URL

	uc, err := fetchUpstreamMetadata("rand", "0.8.5")
	require.NoError(t, err)

	meta, err := json.Marshal(uc.meta)
	require.NoError(t, err)

	// The load-bearing property: what we synthesise is what the PUBLISH path
	// parses. If the wire framing or a field name drifts, the cache stops being
	// able to hold anything and this fails.
	cp, err := cargo_module.ParsePackage(io.MultiReader(
		bytes.NewReader(wireHeader(meta, int64(len(crate)))),
		bytes.NewReader(crate),
	))
	require.NoError(t, err)

	assert.Equal(t, "rand", cp.Name)
	assert.Equal(t, "0.8.5", cp.Version)
	assert.Equal(t, "MIT OR Apache-2.0", cp.Metadata.License)
	assert.Equal(t, "https://docs.rs/rand", cp.Metadata.DocumentationURL)
	assert.Equal(t, "https://rust-random.github.io/book", cp.Metadata.ProjectURL)
	assert.Equal(t, "https://github.com/rust-random/rand", cp.Metadata.RepositoryURL)
	require.Len(t, cp.Metadata.Dependencies, 2)
	assert.Equal(t, "rand_core", cp.Metadata.Dependencies[0].Name)
	assert.Equal(t, "^0.6.0", cp.Metadata.Dependencies[0].Req)
	assert.Nil(t, cp.Metadata.Dependencies[0].Package)

	assert.Equal(t, int64(len(crate)), cp.ContentSize)
	content, err := io.ReadAll(cp.Content)
	require.NoError(t, err)
	assert.Equal(t, crate, content)
}

func TestCacheFromUpstreamRefusesBeforeReachingOut(t *testing.T) {
	defer func(u, p string) {
		setting.Packages.CargoUpstream, setting.Packages.CargoPrivate = u, p
	}(setting.Packages.CargoUpstream, setting.Packages.CargoPrivate)

	srv, seen := upstreamTestServer(t, []byte("CRATEBYTES"))

	// A nil context is deliberate: every one of these refusals must happen
	// BEFORE anything is fetched or stored, so nothing may touch the request. A
	// future change that looks at the context first panics here.
	setting.Packages.CargoUpstream = ""
	setting.Packages.CargoPrivate = "hanzo-"
	require.ErrorIs(t, cacheFromUpstream(nil, "rand", "0.8.5"), packages_model.ErrPackageNotExist)

	setting.Packages.CargoUpstream = srv.URL
	require.ErrorIs(t, cacheFromUpstream(nil, "rand", ""), packages_model.ErrPackageNotExist)
	require.ErrorIs(t, cacheFromUpstream(nil, "", "0.8.5"), packages_model.ErrPackageNotExist)
	// The private guard, on the path that matters: not one byte leaves.
	require.ErrorIs(t, cacheFromUpstream(nil, "hanzo-node", "0.8.5"), packages_model.ErrPackageNotExist)
	require.ErrorIs(t, cacheFromUpstream(nil, "hanzo_node", "0.8.5"), packages_model.ErrPackageNotExist)

	assert.Empty(t, *seen, "a refused crate must never reach the upstream")
}

func TestCacheFromUpstreamRejectsChecksumMismatch(t *testing.T) {
	defer func(u, p string) {
		setting.Packages.CargoUpstream, setting.Packages.CargoPrivate = u, p
	}(setting.Packages.CargoUpstream, setting.Packages.CargoPrivate)
	setting.Packages.CargoPrivate = ""
	setting.AppDataPath = t.TempDir() // the crate is buffered before it can be hashed

	// The metadata advertises the SHA256 of one crate; the file endpoint serves
	// different bytes. Caching must stop before the store, so the mismatch never
	// becomes a package version.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte("CRATEBYTES"))
		switch r.URL.Path {
		case "/api/v1/crates/rand/0.8.5":
			fmt.Fprintf(w, upstreamVersionJSON, hex.EncodeToString(sum[:]))
		case "/api/v1/crates/rand/0.8.5/dependencies":
			io.WriteString(w, upstreamDependenciesJSON)
		default:
			io.WriteString(w, "TAMPERED")
		}
	}))
	defer srv.Close()
	setting.Packages.CargoUpstream = srv.URL

	err := cacheFromUpstream(nil, "rand", "0.8.5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
}
