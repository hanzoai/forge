// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"fmt"
	"github.com/hanzoai/git/modules/web"
	"net/http"
	"strings"

	"github.com/hanzoai/git/modules/cache"
	"github.com/hanzoai/git/modules/gtprof"
	"github.com/hanzoai/git/modules/httplib"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/public"
	"github.com/hanzoai/git/modules/reqctx"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/web/routing"
	"github.com/hanzoai/git/services/context"

	"github.com/chi-middleware/proxy"
	"github.com/hanzoai/git/modules/session/chi"
)

// ProtocolMiddlewares returns HTTP protocol related middlewares, and it provides a global panic recovery
func ProtocolMiddlewares() (handlers []any) {
	// the order is important
	handlers = append(handlers, RoutePathHandler())      // route on the escaped path
	handlers = append(handlers, RequestContextHandler()) //	prepare the context and panic recovery
	handlers = append(handlers, SecurityHeadersHandler())

	if setting.ReverseProxyLimit > 0 && len(setting.ReverseProxyTrustedProxies) > 0 {
		handlers = append(handlers, ForwardedHeadersHandler(setting.ReverseProxyLimit, setting.ReverseProxyTrustedProxies))
	}

	handlers = append(handlers, routing.NewRequestInfoHandler())

	if setting.IsAccessLogEnabled() {
		handlers = append(handlers, context.AccessLogger())
	}

	if !setting.IsProd {
		handlers = append(handlers, public.ViteDevMiddleware)
	}

	return handlers
}

// SecurityHeadersHandler sets headers globally for every response that leaves Gitea.
func SecurityHeadersHandler() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			if setting.Security.XContentTypeOptions != "unset" {
				resp.Header().Set("X-Content-Type-Options", setting.Security.XContentTypeOptions)
			}
			if setting.Security.XFrameOptions != "unset" {
				resp.Header().Set("X-Frame-Options", setting.Security.XFrameOptions)
			}
			next.ServeHTTP(resp, req)
		})
	}
}

func RequestContextHandler() func(h http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(respOrig http.ResponseWriter, req *http.Request) {
			// this response writer might not be the same as the one in context.Base.Resp
			// because there might be a "gzip writer" in the middle, so the "written size" here is the compressed size
			respWriter := context.WrapResponseWriter(respOrig)

			profDesc := fmt.Sprintf("HTTP: %s %s", req.Method, req.RequestURI)
			ctx, finished := reqctx.NewRequestContext(req.Context(), profDesc)
			defer finished()

			ctx, span := gtprof.GetTracer().Start(ctx, gtprof.TraceSpanHTTP)
			req = req.WithContext(ctx)
			defer func() {
				span.SetAttributeString(gtprof.TraceAttrHTTPRoute, web.GetRouteContext(req.Context()).RoutePattern())
				span.End()
			}()

			defer func() {
				if recovered := recover(); recovered != nil {
					renderPanicErrorPage(respWriter, req, recovered) // it should never panic, and it handles the stack trace internally
				}
			}()

			ds := reqctx.GetRequestDataStore(ctx)
			req = req.WithContext(cache.WithCacheContext(ctx))
			ds.SetContextValue(httplib.RequestContextKey, req)
			ds.AddCleanUp(func() {
				// TODO: GOLANG-HTTP-TMPDIR: Golang saves the uploaded files to temp directory (TMPDIR) when parsing multipart-form.
				// The "req" might have changed due to the new "req.WithContext" calls
				// For example: in NewBaseContext, a new "req" with context is created, and the multipart-form is parsed there.
				// So we always use the latest "req" from the data store.
				ctxReq := ds.GetContextValue(httplib.RequestContextKey).(*http.Request)
				if ctxReq.MultipartForm != nil {
					_ = ctxReq.MultipartForm.RemoveAll() // remove the temp files buffered to tmp directory
				}
			})
			next.ServeHTTP(respWriter, req)
		})
	}
}

// RoutePathHandler makes the mux route on the ESCAPED path.
//
// Without it a "%2f" inside a path parameter is decoded before routing and
// splits the segment, so a branch called "feat/x" routes as two segments and
// matches the wrong handler — or nothing.
func RoutePathHandler() func(h http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			rc := web.GetRouteContext(req.Context())
			if rc == nil {
				rc = web.NewRouteContext()
				rc.RouteMethod = req.Method
				req = web.WithRouteContext(req, rc)
			}
			if req.URL.RawPath == "" {
				rc.RoutePath = req.URL.EscapedPath()
			} else {
				rc.RoutePath = req.URL.RawPath
			}
			next.ServeHTTP(resp, req)
		})
	}
}

func ForwardedHeadersHandler(limit int, trustedProxies []string) func(h http.Handler) http.Handler {
	opt := proxy.NewForwardedHeadersOptions().WithForwardLimit(limit).ClearTrustedProxies()
	for _, n := range trustedProxies {
		if !strings.Contains(n, "/") {
			opt.AddTrustedProxy(n)
		} else {
			opt.AddTrustedNetwork(n)
		}
	}
	return proxy.ForwardedHeaders(opt)
}

func MustInitSessioner() func(next http.Handler) http.Handler {
	// TODO: CHI-SESSION-GOB-REGISTER: chi-session has a design problem: it calls gob.Register for "Set"
	// But if the server restarts, then the first "Get" will fail to decode the previously stored session data because the structs are not registered yet.
	// So each package should make sure their structs are registered correctly during startup for session storage.

	middleware, err := session.Sessioner(session.Options{
		Provider:       setting.SessionConfig.Provider,
		ProviderConfig: setting.SessionConfig.ProviderConfig,
		CookieName:     setting.SessionConfig.CookieName,
		CookiePath:     setting.SessionConfig.CookiePath,
		Gclifetime:     setting.SessionConfig.Gclifetime,
		Maxlifetime:    setting.SessionConfig.Maxlifetime,
		Secure:         setting.SessionConfig.Secure,
		SameSite:       setting.SessionConfig.SameSite,
		Domain:         setting.SessionConfig.Domain,

		// in the future, if websocket is used, the websocket handler should manage its own session sync (release)
		IgnoreReleaseForWebSocket: true,
	})
	if err != nil {
		log.Fatal("common.Sessioner failed: %v", err)
	}
	return middleware
}
