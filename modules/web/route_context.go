// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"net/http"
	"strings"
)

// RouteContext carries what a matched route knows: the method it matched, the
// parameters it captured, and the patterns it matched along the way.
//
// This is data, not routing. The regex matching that fills it lives in
// routerPathMatcher, which this package has always owned — chi was only holding
// these three fields on the way through, so depending on a router for them made
// the router look load-bearing when it was not.
type RouteContext struct {
	// RouteMethod is the method the route was matched with. It can differ from
	// req.Method: a HEAD is routed as a GET.
	RouteMethod string

	// RoutePatterns records every pattern matched to reach the handler, in
	// order, so nested groups can be reconstructed for logging and metrics.
	RoutePatterns []string

	// RoutePath overrides the path the mux routes on. The normalising middleware
	// sets it so an escaped path routes as its decoded form.
	RoutePath string

	params map[string]string
	order  []string // insertion order; a repeated name keeps its first position
}

type routeCtxKey struct{}

// RouteCtxKey is the context key a RouteContext is stored under. Exported so
// test helpers can build a routed-looking request without a router.
var RouteCtxKey = routeCtxKey{}

// NewRouteContext returns an empty RouteContext ready to accumulate parameters.
func NewRouteContext() *RouteContext {
	return &RouteContext{params: map[string]string{}}
}

// WithRouteContext attaches rc to req so handlers below can read the captured
// parameters.
func WithRouteContext(req *http.Request, rc *RouteContext) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), routeCtxKey{}, rc))
}

// GetRouteContext returns the RouteContext on ctx, or nil when the request was
// not routed through this package.
func GetRouteContext(ctx context.Context) *RouteContext {
	rc, _ := ctx.Value(routeCtxKey{}).(*RouteContext)
	return rc
}

// SetParam records a captured parameter.
//
// Last write wins, and the first position is kept. A nested group can re-capture
// a name the outer group already set — the inner value is the more specific one,
// but reordering the parameters would change what a caller iterating them sees.
func (rc *RouteContext) SetParam(name, value string) {
	if rc.params == nil {
		rc.params = map[string]string{}
	}
	if _, seen := rc.params[name]; !seen {
		rc.order = append(rc.order, name)
	}
	rc.params[name] = value
}

// Param returns a captured parameter, or "" when the route did not capture it.
func (rc *RouteContext) Param(name string) string {
	if rc == nil {
		return ""
	}
	return rc.params[name]
}

// RoutePattern is the full pattern that matched, with the wildcards that
// mounting contributes collapsed away.
//
// Each Mount appends its own "/*" to RoutePatterns, so a plain join yields
// "/v1/*/repos/{username}/*/branches/..." — the stars are an artefact of how the
// route was assembled, not part of the route. They are removed so the value is
// the pattern a reader would have written, which is what logs and metrics group
// by.
func (rc *RouteContext) RoutePattern() string {
	if rc == nil {
		return ""
	}
	pattern := strings.Join(rc.RoutePatterns, "")
	for strings.Contains(pattern, "/*/") {
		pattern = strings.ReplaceAll(pattern, "/*/", "/")
	}
	if pattern != "/" {
		pattern = strings.TrimSuffix(pattern, "//")
		pattern = strings.TrimSuffix(pattern, "/")
	}
	return pattern
}

// deleteParam removes a parameter. Matching backtracks, so a capture made on a
// branch that then fails must not be visible to the branch tried next.
func (rc *RouteContext) deleteParam(name string) {
	delete(rc.params, name)
	for i, n := range rc.order {
		if n == name {
			rc.order = append(rc.order[:i], rc.order[i+1:]...)
			break
		}
	}
}

// ParamNames returns the captured names in the order they were first set.
func (rc *RouteContext) ParamNames() []string {
	if rc == nil {
		return nil
	}
	return rc.order
}
