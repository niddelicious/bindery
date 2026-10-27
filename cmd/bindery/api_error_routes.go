package main

import (
	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
)

// registerAPIErrorHandlers gives an API sub-router (/api, /api/v1) JSON 404
// and 405 responses in the same {"error": ...} shape as every handler, in
// place of chi's plain text "404 page not found" and empty 405.
//
// It is called on the sub-router inside its Route closure, not on the root:
// the root's "/*" is the SPA fallback, and /opds, /auth and /bridge keep
// their own behaviour. chi runs a sub-router's NotFound handler after that
// router's middleware, so an unauthenticated probe of an unknown API path
// still gets the auth layer's 401 rather than learning which paths exist.
func registerAPIErrorHandlers(r chi.Router) {
	r.NotFound(api.NotFoundJSON)
	r.MethodNotAllowed(api.MethodNotAllowedJSON(r))
}
