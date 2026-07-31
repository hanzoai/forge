// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The cases that decide whether a forge routes correctly. Each one is a way the
// tree can be wrong that a passing build would not reveal.
func TestMuxMatching(t *testing.T) {
	type route struct{ method, pattern string }
	routes := []route{
		{"GET", "/user/settings"},         // static must beat {username}
		{"GET", "/{username}"},            //
		{"GET", "/{username}/{reponame}"}, // two params, then...
		{"GET", "/{username}/{reponame}/issues/{id}"},
		{"GET", "/repo/{sha:[a-f0-9]{7,40}}"}, // regexp-constrained
		{"GET", "/repo/latest"},               // static beside it
		{"GET", "/cran/src/PACKAGES{format}"}, // parameter mid-segment
		{"GET", "/files/*"},                   // trailing wildcard
		{"POST", "/{username}/{reponame}"},    // same path, other method
	}
	m := NewMux()
	for _, r := range routes {
		pattern := r.pattern
		m.Method(r.method, pattern, http.HandlerFunc(func(resp http.ResponseWriter, _ *http.Request) {
			resp.Header().Set("X-Pattern", pattern)
		}))
	}

	for _, tc := range []struct {
		name, method, path, wantPattern string
		wantParams                      map[string]string
	}{
		{"static beats param", "GET", "/user/settings", "/user/settings", nil},
		{"param when no static", "GET", "/someone", "/{username}", map[string]string{"username": "someone"}},
		{"two params", "GET", "/o/r", "/{username}/{reponame}", map[string]string{"username": "o", "reponame": "r"}},
		{"deep under params", "GET", "/o/r/issues/7", "/{username}/{reponame}/issues/{id}",
			map[string]string{"username": "o", "reponame": "r", "id": "7"}},
		{"regexp matches", "GET", "/repo/abcdef1234", "/repo/{sha:[a-f0-9]{7,40}}", map[string]string{"sha": "abcdef1234"}},
		{"static beside regexp", "GET", "/repo/latest", "/repo/latest", nil},
		{"mid-segment param", "GET", "/cran/src/PACKAGES.gz", "/cran/src/PACKAGES{format}", map[string]string{"format": ".gz"}},
		{"wildcard captures rest", "GET", "/files/a/b/c.txt", "/files/*", map[string]string{"*": "a/b/c.txt"}},
		{"method is routed", "POST", "/o/r", "/{username}/{reponame}", map[string]string{"username": "o", "reponame": "r"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rc := NewRouteContext()
			req = WithRouteContext(req, rc)
			resp := httptest.NewRecorder()
			m.ServeHTTP(resp, req)

			assert.Equal(t, tc.wantPattern, resp.Header().Get("X-Pattern"), "routed to the wrong handler")
			for k, v := range tc.wantParams {
				assert.Equal(t, v, rc.Param(k), "param %q", k)
			}
		})
	}

	t.Run("regexp declines and the next candidate wins", func(t *testing.T) {
		// "zzz" is not hex so /repo/{sha:...} must decline — but declining does
		// not mean 404: /{username}/{reponame} still matches "/repo/zzz", and it
		// should, because "repo" is a legal username. The assertion is that the
		// constrained route did NOT capture it.
		req := httptest.NewRequest("GET", "/repo/zzz", nil)
		rc := NewRouteContext()
		req = WithRouteContext(req, rc)
		resp := httptest.NewRecorder()
		m.ServeHTTP(resp, req)

		assert.Equal(t, "/{username}/{reponame}", resp.Header().Get("X-Pattern"))
		assert.Empty(t, rc.Param("sha"), "the constrained route captured a value it should have rejected")
		assert.Equal(t, "repo", rc.Param("username"))
	})

	t.Run("unregistered method is not served by another", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", "/user/settings", nil)
		req = WithRouteContext(req, NewRouteContext())
		resp := httptest.NewRecorder()
		m.ServeHTTP(resp, req)
		assert.Equal(t, http.StatusNotFound, resp.Code)
	})
}

// A failed branch must leave no captured parameters behind, or the branch tried
// next inherits values from a route that did not match.
func TestMuxBacktrackingLeavesNoParams(t *testing.T) {
	m := NewMux()
	m.Method("GET", "/{owner}/{repo}/tree", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	m.Method("GET", "/{single}", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest("GET", "/onlyone", nil)
	rc := NewRouteContext()
	req = WithRouteContext(req, rc)
	m.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "onlyone", rc.Param("single"))
	assert.Empty(t, rc.Param("owner"), "owner was captured by a branch that failed")
	assert.Equal(t, []string{"single"}, rc.ParamNames())
}

func TestMuxHeadFallsBackToGet(t *testing.T) {
	m := NewMux()
	m.Method("GET", "/thing", http.HandlerFunc(func(resp http.ResponseWriter, _ *http.Request) {
		resp.Header().Set("X-Hit", "get")
	}))
	req := httptest.NewRequest("HEAD", "/thing", nil)
	req = WithRouteContext(req, NewRouteContext())
	resp := httptest.NewRecorder()
	m.ServeHTTP(resp, req)
	assert.Equal(t, "get", resp.Header().Get("X-Hit"))
}

// A route constraint may contain "/", and splitting the pattern on every "/"
// cuts it in half. This is the real registration from routers/web/githttp.go;
// before splitPattern it made compileSegment panic at startup, so the forge
// never opened a listener and git.hanzo.ai served 503.
func TestMuxPatternConstraintContainingSlash(t *testing.T) {
	m := NewMux()
	m.Method("GET", "/objects/info/{file:[^/]*}", http.HandlerFunc(func(resp http.ResponseWriter, _ *http.Request) {
		resp.WriteHeader(http.StatusTeapot)
	}))

	// The constraint must survive as ONE segment, not "{file:[^" + "]*}".
	assert.Equal(t, []string{"objects", "info", "{file:[^/]*}"}, splitPattern("/objects/info/{file:[^/]*}"))

	resp := httptest.NewRecorder()
	m.ServeHTTP(resp, httptest.NewRequest("GET", "/objects/info/packs", nil))
	assert.Equal(t, http.StatusTeapot, resp.Code)

	// [^/]* must still refuse to span a separator.
	resp = httptest.NewRecorder()
	m.ServeHTTP(resp, httptest.NewRequest("GET", "/objects/info/a/b", nil))
	assert.NotEqual(t, http.StatusTeapot, resp.Code)
}

// A constraint may nest one level of braces, so the split tracks depth rather
// than a boolean "inside a brace" flag.
func TestSplitPatternNestedBraces(t *testing.T) {
	assert.Equal(t, []string{"a", "{n:[0-9]{2}}", "b"}, splitPattern("/a/{n:[0-9]{2}}/b"))
	assert.Equal(t, []string{"repo", "{name}"}, splitPattern("/repo/{name}"))
	assert.Nil(t, splitPattern("/"))
}

// splitPath stays brace-blind: in a REQUEST path "{" is an ordinary character
// and "/" always separates, so it must NOT inherit splitPattern's behaviour.
func TestSplitPathIsNotPatternAware(t *testing.T) {
	assert.Equal(t, []string{"objects", "info", "{file:[^", "]*}"}, splitPath("/objects/info/{file:[^/]*}"))
}
