// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package conan

import (
	"net/http"
	"net/http/httptest"
	"testing"

	conan_module "github.com/hanzoai/git/modules/packages/conan"
	"github.com/hanzoai/git/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpstreamEnabled(t *testing.T) {
	defer func(v string) { setting.Packages.ConanUpstream = v }(setting.Packages.ConanUpstream)

	// Empty means publish-only -- the behaviour every deployment has until
	// someone opts in. Whitespace must not accidentally enable it.
	setting.Packages.ConanUpstream = ""
	assert.False(t, upstreamEnabled())
	setting.Packages.ConanUpstream = "   "
	assert.False(t, upstreamEnabled())

	setting.Packages.ConanUpstream = "https://center.conan.io"
	assert.True(t, upstreamEnabled())
}

func TestIsPrivateRecipe(t *testing.T) {
	defer func(v string) { setting.Packages.ConanPrivate = v }(setting.Packages.ConanPrivate)
	setting.Packages.ConanPrivate = "hanzo-,lux-,luxcpp,zoo-"

	// Private names must never reach a public remote -- the name alone leaks an
	// internal library that may not be public yet.
	assert.True(t, isPrivateRecipe("lux-crypto"))
	assert.True(t, isPrivateRecipe("luxcpp"))
	assert.True(t, isPrivateRecipe("hanzo-runtime"))

	// Public recipes still cache.
	assert.False(t, isPrivateRecipe("zlib"))
	assert.False(t, isPrivateRecipe("boost"))
	// Near-misses must NOT be treated as private (no accidental over-blocking).
	assert.False(t, isPrivateRecipe("luxon"))
	assert.False(t, isPrivateRecipe("hanzoai"))
	assert.False(t, isPrivateRecipe("zoology"))

	// Empty config blocks nothing.
	setting.Packages.ConanPrivate = ""
	assert.False(t, isPrivateRecipe("lux-crypto"))
}

func TestHasConcreteRevision(t *testing.T) {
	newRef := func(t *testing.T, revision string) *conan_module.RecipeReference {
		t.Helper()
		rref, err := conan_module.NewRecipeReference("zlib", "1.3.1", "", "", revision)
		require.NoError(t, err)
		return rref
	}

	// Only a real revision names one immutable thing. No revision at all, or the
	// "0" sentinel every v1 URL arrives as, is a question -- not something that
	// can be fetched without guessing.
	assert.False(t, hasConcreteRevision(newRef(t, "")))
	assert.False(t, hasConcreteRevision(newRef(t, conan_module.DefaultRevision)))
	assert.True(t, hasConcreteRevision(newRef(t, "6a7e1a92e3c5e1d5f3b8b8f6e9d0c1a2")))
}

func TestUpstreamRecipePath(t *testing.T) {
	rref, err := conan_module.NewRecipeReference("zlib", "1.3.1", "", "", "abc123")
	require.NoError(t, err)
	elem, ok := upstreamRecipePath(rref)
	require.True(t, ok)
	// A recipe with no user/channel is "_/_" upstream, not an empty segment.
	assert.Equal(t, "v2/conans/zlib/1.3.1/_/_/revisions/abc123", elem)

	rref, err = conan_module.NewRecipeReference("zlib", "1.3.1", "lux", "stable", "abc123")
	require.NoError(t, err)
	elem, ok = upstreamRecipePath(rref)
	require.True(t, ok)
	assert.Equal(t, "v2/conans/zlib/1.3.1/lux/stable/revisions/abc123", elem)

	// NewRecipeReference only requires the version to be non-empty, so a dot
	// segment gets this far. It must never become part of an upstream URL.
	rref, err = conan_module.NewRecipeReference("zlib", "..", "", "", "abc123")
	require.NoError(t, err)
	_, ok = upstreamRecipePath(rref)
	assert.False(t, ok)

	rref, err = conan_module.NewRecipeReference("zlib", "1.3.1/../../etc", "", "", "abc123")
	require.NoError(t, err)
	_, ok = upstreamRecipePath(rref)
	assert.False(t, ok)
}

func TestUpstreamFetch(t *testing.T) {
	defer func(v string) { setting.Packages.ConanUpstream = v }(setting.Packages.ConanUpstream)

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path == "/v2/conans/zlib/1.3.1/_/_/revisions/abc123/files/conanfile.py" {
			w.Write([]byte("from conan import ConanFile"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// A trailing slash on the configured base must not produce a doubled slash.
	setting.Packages.ConanUpstream = srv.URL + "/"
	body, _, err := upstreamFetch("v2/conans/zlib/1.3.1/_/_/revisions/abc123/files/conanfile.py")
	require.NoError(t, err)
	defer body.Close()
	assert.Equal(t, "/v2/conans/zlib/1.3.1/_/_/revisions/abc123/files/conanfile.py", gotPath)

	// A non-200 is an error, never an empty body silently cached as a recipe.
	setting.Packages.ConanUpstream = srv.URL
	_, _, err = upstreamFetch("v2/conans/zlib/9.9.9/_/_/revisions/abc123/files/conanfile.py")
	require.Error(t, err)
}

func TestUpstreamRecipeFileList(t *testing.T) {
	defer func(v string) { setting.Packages.ConanUpstream = v }(setting.Packages.ConanUpstream)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/conans/zlib/1.3.1/_/_/revisions/abc123/files" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"files": {"conanfile.py": {}, "conanmanifest.txt": {}, "conan_export.tgz": {}, "evil.sh": {}}}`))
	}))
	defer srv.Close()
	setting.Packages.ConanUpstream = srv.URL

	names, err := upstreamRecipeFileList("v2/conans/zlib/1.3.1/_/_/revisions/abc123")
	require.NoError(t, err)
	// Sorted, and filtered to what the upload path accepts -- the cache can only
	// ever hold files this registry would have taken from a publish.
	assert.Equal(t, []string{"conan_export.tgz", "conanfile.py", "conanmanifest.txt"}, names)

	// An upstream that does not have the revision is an error, so the caller
	// falls back to the ordinary not-found.
	_, err = upstreamRecipeFileList("v2/conans/zlib/9.9.9/_/_/revisions/abc123")
	require.Error(t, err)
}
