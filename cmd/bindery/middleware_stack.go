package main

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/metrics"
)

// useCoreMiddleware installs the router wide middleware, in order. It is its
// own function so a test can serve a handler behind the very same stack the
// server uses: every wrapper in it has to pass http.NewResponseController
// through to the server's writer (a file download moves its write deadline
// that way), and a hand copied stack would not notice a new wrapper.
func useCoreMiddleware(r chi.Router, frameAncestors string) {
	r.Use(middleware.RequestID)
	// BINDERY_TRUSTED_PROXY (optional): comma-separated IP/CIDR list of
	// reverse proxies permitted to set X-Forwarded-For. When unset, XFF
	// headers are ignored — required for local-only auth mode to be safe
	// against on-network spoofing.
	r.Use(trustedProxyMiddleware())
	// api.Recoverer, not chi's: it logs the panic and stack through slog (so
	// they reach the in-app log buffer) and answers JSON with the request id.
	// It must stay below middleware.RequestID.
	r.Use(api.Recoverer)
	r.Use(compressUnlessDownload(5))
	r.Use(api.SecurityHeaders(frameAncestors))
	// Cap JSON / form request bodies at 1 MiB by default so an authenticated
	// client cannot pin the process by streaming a multi-gigabyte body into
	// json.Decode. Routes that legitimately accept larger payloads opt in via
	// api.WithMaxBody on the chi sub-route (see /migrate/* below).
	// PreserveRawBody must run first so the per-route override can re-wrap
	// the raw body with a higher cap; without it the override would chain a
	// larger MaxBytesReader on top of the smaller default and the inner cap
	// would silently win.
	r.Use(api.PreserveRawBody)
	r.Use(api.MaxRequestBody)
	r.Use(metrics.HTTPMiddleware(routeTemplate))
}
