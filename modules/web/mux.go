// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Mux routes requests to handlers. It replaces the chi router.
//
// The tree is over path SEGMENTS, and a node tries its children in order of how
// specific they are: an exact segment, then patterned segments in the order
// they were registered, then a trailing wildcard. That ordering is the whole
// contract — "/user/settings" must win over "/user/{name}" no matter which was
// registered first, or a user called "settings" takes over the settings page.
//
// Matching backtracks. A segment can match here and fail deeper, and the next
// candidate at this level still has to be tried: "/{username}/{reponame}" and
// "/{username}/settings" share a first segment, so failing the second segment
// under one is not failure of the route.
//
// Four segment shapes appear in this codebase's 1333 routes, and all four are
// supported deliberately rather than by accident:
//
//	users            static
//	{id}             whole-segment parameter
//	{sha:[a-f0-9]+}  parameter with a regexp constraint (43 of them)
//	PACKAGES{format} parameter that is only PART of a segment (CRAN indexes)
//	*                trailing wildcard, captured as the "*" parameter
type Mux struct {
	root       *muxNode
	middleware []func(http.Handler) http.Handler
	notFound   http.Handler
}

type muxNode struct {
	static map[string]*muxNode

	// patterned children, in registration order. Order is preserved rather than
	// sorted because two patterns can both match and the earlier registration is
	// the one the author meant to win.
	patterned []*muxPatternChild

	wildcard *muxNode // trailing "*"

	// handlers by method. "" is the any-method handler, consulted only after an
	// exact method miss so that a specific registration always beats a catch-all.
	handlers map[string]http.Handler

	// mounted is a subtree handler: it consumes the remainder of the path.
	mounted http.Handler

	pattern string // the registered pattern reaching this node, for RoutePattern
}

type muxPatternChild struct {
	node *muxNode

	// name is the parameter this segment captures.
	name string

	// re is nil for a plain "{name}" segment, which matches a whole segment and
	// needs no regexp — the common case by far (577 of them), so it does not pay
	// for one.
	re *regexp.Regexp
}

// NewMux returns an empty Mux.
func NewMux() *Mux {
	return &Mux{root: newMuxNode()}
}

func newMuxNode() *muxNode {
	return &muxNode{static: map[string]*muxNode{}, handlers: map[string]http.Handler{}}
}

// Use adds a middleware run before routing, on every request including misses.
func (m *Mux) Use(mw func(http.Handler) http.Handler) {
	m.middleware = append(m.middleware, mw)
}

// NotFound sets the handler for requests that match no route.
func (m *Mux) NotFound(h http.Handler) { m.notFound = h }

// NotFoundHandler returns the configured miss handler, or the standard one.
func (m *Mux) NotFoundHandler() http.Handler {
	if m.notFound != nil {
		return m.notFound
	}
	return http.HandlerFunc(http.NotFound)
}

// Method registers h for one method. An empty method matches any.
func (m *Mux) Method(method, pattern string, h http.Handler) {
	n := m.addRoute(pattern)
	n.handlers[strings.ToUpper(method)] = h
}

// Mount attaches h to everything under pattern. The subtree handler sees the
// full path: this forge's mounted routers match on it themselves.
func (m *Mux) Mount(pattern string, h http.Handler) {
	n := m.addRoute(strings.TrimSuffix(pattern, "/"))
	n.mounted = h
}

// addRoute walks the tree to the node for pattern, creating it as needed.
func (m *Mux) addRoute(pattern string) *muxNode {
	cur := m.root
	for _, seg := range splitPath(pattern) {
		switch {
		case seg == "*":
			if cur.wildcard == nil {
				cur.wildcard = newMuxNode()
			}
			cur = cur.wildcard
		case strings.ContainsAny(seg, "{}"):
			cur = cur.childForPattern(seg)
		default:
			next := cur.static[seg]
			if next == nil {
				next = newMuxNode()
				cur.static[seg] = next
			}
			cur = next
		}
	}
	cur.pattern = pattern
	return cur
}

// childForPattern finds or creates the patterned child for one segment.
func (n *muxNode) childForPattern(seg string) *muxNode {
	name, re := compileSegment(seg)
	for _, c := range n.patterned {
		sameRe := (c.re == nil) == (re == nil) && (re == nil || c.re.String() == re.String())
		if c.name == name && sameRe {
			return c.node
		}
	}
	child := &muxPatternChild{node: newMuxNode(), name: name, re: re}
	n.patterned = append(n.patterned, child)
	return child.node
}

var segmentParamRe = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)(?::([^{}]*(?:\{[0-9,]*\}[^{}]*)*))?\}`)

// compileSegment turns one pattern segment into a parameter name and, when the
// segment is more than a bare "{name}", a regexp anchored to the whole segment.
func compileSegment(seg string) (name string, re *regexp.Regexp) {
	loc := segmentParamRe.FindStringSubmatchIndex(seg)
	if loc == nil {
		// A brace that is not a parameter would silently become a static segment
		// that can never match, so refuse it at registration instead.
		panic("web: not a valid route segment: " + seg)
	}
	sub := segmentParamRe.FindStringSubmatch(seg)
	name, constraint := sub[1], sub[2]

	whole := loc[0] == 0 && loc[1] == len(seg)
	if whole && constraint == "" {
		return name, nil // plain {name}: no regexp needed
	}

	var b strings.Builder
	b.WriteString("^")
	b.WriteString(regexp.QuoteMeta(seg[:loc[0]]))
	if constraint == "" {
		b.WriteString("(.+)")
	} else {
		b.WriteString("(" + constraint + ")")
	}
	b.WriteString(regexp.QuoteMeta(seg[loc[1]:]))
	b.WriteString("$")
	return name, regexp.MustCompile(b.String())
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// ServeHTTP routes the request.
func (m *Mux) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	h := http.Handler(http.HandlerFunc(m.route))
	for i := len(m.middleware) - 1; i >= 0; i-- {
		h = m.middleware[i](h)
	}
	h.ServeHTTP(resp, req)
}

func (m *Mux) route(resp http.ResponseWriter, req *http.Request) {
	rc := GetRouteContext(req.Context())
	if rc == nil {
		rc = NewRouteContext()
		req = WithRouteContext(req, rc)
	}
	rc.RouteMethod = req.Method

	// The path the mux routes on can be overridden — the normalising middleware
	// sets it so that an escaped path routes as its decoded form.
	path := rc.RoutePath
	if path == "" {
		path = req.URL.EscapedPath()
		if req.URL.RawPath != "" {
			path = req.URL.RawPath
		}
	}

	h, pattern := m.root.match(splitPath(path), req.Method, rc)
	if h == nil {
		m.NotFoundHandler().ServeHTTP(resp, req)
		return
	}
	rc.RoutePatterns = append(rc.RoutePatterns, pattern)
	h.ServeHTTP(resp, req)
}

// match walks segs from n. It records captured parameters into rc as it goes and
// removes them again when a branch fails, so a failed attempt leaves nothing
// behind for the branch tried next.
func (n *muxNode) match(segs []string, method string, rc *RouteContext) (http.Handler, string) {
	if len(segs) == 0 {
		if h := n.handlerFor(method); h != nil {
			return h, n.pattern
		}
		// "/*" matches "/" with an empty remainder. Requiring a segment would
		// make a catch-all decline the one path it most obviously covers.
		if n.wildcard != nil {
			if h := n.wildcard.handlerFor(method); h != nil {
				rc.SetParam("*", "")
				return h, n.wildcard.pattern
			}
			if n.wildcard.mounted != nil {
				rc.SetParam("*", "")
				return n.wildcard.mounted, n.wildcard.pattern
			}
		}
		// A mount registered at exactly this path still serves it.
		if n.mounted != nil {
			return n.mounted, n.pattern
		}
		return nil, ""
	}

	seg, rest := segs[0], segs[1:]

	if next, ok := n.static[seg]; ok {
		if h, p := next.match(rest, method, rc); h != nil {
			return h, p
		}
	}

	for _, c := range n.patterned {
		value := seg
		if c.re != nil {
			sub := c.re.FindStringSubmatch(seg)
			if sub == nil {
				continue
			}
			value = sub[1]
		}
		saved, had := rc.params[c.name]
		rc.SetParam(c.name, value)
		if h, p := c.node.match(rest, method, rc); h != nil {
			return h, p
		}
		if had {
			rc.params[c.name] = saved
		} else {
			rc.deleteParam(c.name)
		}
	}

	if n.wildcard != nil {
		rc.SetParam("*", strings.Join(segs, "/"))
		if h := n.wildcard.handlerFor(method); h != nil {
			return h, n.wildcard.pattern
		}
		if n.wildcard.mounted != nil {
			return n.wildcard.mounted, n.wildcard.pattern
		}
		rc.deleteParam("*")
	}

	// A mount consumes whatever is left, so it is tried once the more specific
	// children have all declined.
	if n.mounted != nil {
		rc.SetParam("*", strings.Join(segs, "/"))
		return n.mounted, n.pattern
	}
	return nil, ""
}

func (n *muxNode) handlerFor(method string) http.Handler {
	if h, ok := n.handlers[method]; ok {
		return h
	}
	// HEAD falls back to GET, as net/http does for a body-less response.
	if method == http.MethodHead {
		if h, ok := n.handlers[http.MethodGet]; ok {
			return h
		}
	}
	return n.handlers[""]
}

var _ = fmt.Sprintf // keep fmt for future diagnostics without churn
