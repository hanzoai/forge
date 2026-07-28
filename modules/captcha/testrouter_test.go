// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package captcha

import "net/http"

// testRouter is the small router these vendored tests need to exercise the
// middleware. It replaces chi, which was pulled in as a dependency purely so
// upstream's tests could register two routes.
//
// modules/web imports this package, so it cannot be used here — and stdlib
// ServeMux has done method-qualified patterns since Go 1.22, which is all these
// tests ask for.
type testRouter struct {
	mux        *http.ServeMux
	middleware []func(http.Handler) http.Handler
}

func newTestRouter() *testRouter { return &testRouter{mux: http.NewServeMux()} }

// Use adds a middleware, applied outermost-first around every route.
func (r *testRouter) Use(mw func(http.Handler) http.Handler) {
	r.middleware = append(r.middleware, mw)
}

func (r *testRouter) Get(pattern string, h http.HandlerFunc)  { r.mux.HandleFunc("GET "+pattern, h) }
func (r *testRouter) Post(pattern string, h http.HandlerFunc) { r.mux.HandleFunc("POST "+pattern, h) }

func (r *testRouter) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	h := http.Handler(r.mux)
	for i := len(r.middleware) - 1; i >= 0; i-- {
		h = r.middleware[i](h)
	}
	h.ServeHTTP(resp, req)
}
