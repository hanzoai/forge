// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package goproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/git/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEscapeModule(t *testing.T) {
	// The GOPROXY case-encoding. Getting this wrong is the classic bug where a
	// module resolves for one client and 404s for another, so it is pinned.
	cases := []struct{ in, want string }{
		{"github.com/hanzoai/git", "github.com/hanzoai/git"},
		{"github.com/BurntSushi/toml", "github.com/!burnt!sushi/toml"},
		{"github.com/Masterminds/semver", "github.com/!masterminds/semver"},
		{"ALLCAPS", "!a!l!l!c!a!p!s"},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, escapeModule(c.in), "escapeModule(%q)", c.in)
	}
}

func TestUpstreamEnabled(t *testing.T) {
	defer func(v string) { setting.Packages.GoProxyUpstream = v }(setting.Packages.GoProxyUpstream)

	// Empty means publish-only — the behaviour every deployment has until
	// someone opts in. Whitespace must not accidentally enable it.
	setting.Packages.GoProxyUpstream = ""
	assert.False(t, upstreamEnabled())
	setting.Packages.GoProxyUpstream = "   "
	assert.False(t, upstreamEnabled())

	setting.Packages.GoProxyUpstream = "https://proxy.golang.org"
	assert.True(t, upstreamEnabled())
}

func TestIsPrivateModule(t *testing.T) {
	defer func(v string) { setting.Packages.GoProxyPrivate = v }(setting.Packages.GoProxyPrivate)
	setting.Packages.GoProxyPrivate = "github.com/hanzoai/,github.com/luxfi/,git.hanzo.ai/"

	// Private paths must never reach a public proxy -- the path alone leaks a
	// repo name that may not be public yet.
	assert.True(t, isPrivateModule("github.com/hanzoai/git"))
	assert.True(t, isPrivateModule("github.com/luxfi/node"))
	assert.True(t, isPrivateModule("git.hanzo.ai/hanzoai/cloud"))

	// Public modules still cache.
	assert.False(t, isPrivateModule("github.com/stretchr/testify"))
	assert.False(t, isPrivateModule("golang.org/x/net"))
	// A near-miss must NOT be treated as private (no accidental over-blocking).
	assert.False(t, isPrivateModule("github.com/hanzoai-community/thing"))

	// Empty config blocks nothing.
	setting.Packages.GoProxyPrivate = ""
	assert.False(t, isPrivateModule("github.com/hanzoai/git"))
}

func TestUpstreamListVersions(t *testing.T) {
	defer func(u, p string) {
		setting.Packages.GoProxyUpstream, setting.Packages.GoProxyPrivate = u, p
	}(setting.Packages.GoProxyUpstream, setting.Packages.GoProxyPrivate)
	setting.Packages.GoProxyPrivate = "github.com/hanzoai/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/example.com/mod/@v/list" {
			// Blank lines and stray whitespace are real in the wild.
			w.Write([]byte("v1.0.0\nv1.1.0\n\n  v1.2.0  \n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	setting.Packages.GoProxyUpstream = srv.URL

	got, err := upstreamListVersions("example.com/mod")
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.0.0", "v1.1.0", "v1.2.0"}, got)

	// A private module must never be listed upstream -- same leak as a fetch.
	got, err = upstreamListVersions("github.com/hanzoai/secret")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Disabled upstream is not an error, just nothing to add.
	setting.Packages.GoProxyUpstream = ""
	got, err = upstreamListVersions("example.com/mod")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestUpstreamLatest(t *testing.T) {
	defer func(u, p string) {
		setting.Packages.GoProxyUpstream, setting.Packages.GoProxyPrivate = u, p
	}(setting.Packages.GoProxyUpstream, setting.Packages.GoProxyPrivate)
	setting.Packages.GoProxyPrivate = "github.com/hanzoai/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com/mod/@latest":
			w.Write([]byte(`{"Version":"v1.2.0","Time":"2026-01-01T00:00:00Z"}`))
		case "/example.com/empty/@latest":
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	setting.Packages.GoProxyUpstream = srv.URL

	v, err := upstreamLatest("example.com/mod")
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", v)

	// A response with no version must be an error, never an empty version that
	// downstream would treat as a real one.
	_, err = upstreamLatest("example.com/empty")
	require.Error(t, err)

	// Private modules never ask upstream what their latest is.
	_, err = upstreamLatest("github.com/hanzoai/secret")
	require.Error(t, err)
}

func TestUpstreamFetch(t *testing.T) {
	defer func(v string) { setting.Packages.GoProxyUpstream = v }(setting.Packages.GoProxyUpstream)

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path == "/example.com/mod/@v/v1.0.0.zip" {
			w.Write([]byte("ZIPBYTES"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// A trailing slash on the configured base must not produce a doubled slash.
	setting.Packages.GoProxyUpstream = srv.URL + "/"
	body, _, err := upstreamFetch("example.com/mod/@v/v1.0.0.zip")
	require.NoError(t, err)
	defer body.Close()
	assert.Equal(t, "/example.com/mod/@v/v1.0.0.zip", gotPath)

	// A non-200 is an error, never an empty body silently cached as a module.
	setting.Packages.GoProxyUpstream = srv.URL
	_, _, err = upstreamFetch("example.com/mod/@v/v9.9.9.zip")
	require.Error(t, err)
}
