// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package validation

import "net/http"

// testRouter is the small router these vendored tests need to exercise the
// middleware. It replaces chi, which was pulled in as a dependency purely so
// upstream's tests could register two routes.
//
// modules/web imports this package, so it cannot be used here — and stdlib
// ServeMux has done method-qualified patterns since Go 1.22, which is all these
// tests ask for.
type testRouter struct{ mux *http.ServeMux }

func newTestRouter() *testRouter { return &testRouter{mux: http.NewServeMux()} }

func (r *testRouter) Get(pattern string, h http.HandlerFunc)  { r.mux.HandleFunc("GET "+pattern, h) }
func (r *testRouter) Post(pattern string, h http.HandlerFunc) { r.mux.HandleFunc("POST "+pattern, h) }

func (r *testRouter) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(resp, req)
}
