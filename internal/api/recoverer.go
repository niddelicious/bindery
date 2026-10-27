package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5/middleware"
)

// Recoverer replaces chi's middleware.Recoverer. It differs in the three
// ways that matter to an operator and a client:
//
//   - The panic and its stack go through slog, with the request id, so they
//     reach the in-app log buffer (Settings, Logs) and not only stderr.
//   - A request that had not started its response gets the same JSON error
//     shape as every other failure, carrying the request id so a report can be
//     matched to the log line. Neither the panic value nor the stack is sent
//     to the client.
//   - http.ErrAbortHandler is re-panicked untouched: it is how a handler
//     deliberately drops the connection, and net/http expects to see it.
//
// It must sit inside middleware.RequestID so the id is already in the context.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			rvr := recover()
			if rvr == nil {
				return
			}
			if rvr == http.ErrAbortHandler { //nolint:errorlint // net/http compares the sentinel by identity
				panic(rvr)
			}
			reqID := middleware.GetReqID(r.Context())
			slog.Error("panic recovered in request handler",
				"request_id", reqID,
				"method", r.Method,
				"path", r.URL.Path,
				"panic", fmt.Sprint(rvr),
				"stack", string(debug.Stack()),
			)
			// A websocket-style upgrade owns the connection; chi leaves it alone
			// too. And once any of the response is out, a second status line
			// would only corrupt it.
			if r.Header.Get("Connection") == "Upgrade" || ww.Status() != 0 || ww.BytesWritten() != 0 {
				return
			}
			body := map[string]string{"error": "internal server error"}
			if reqID != "" {
				body["request_id"] = reqID
			}
			writeJSON(w, http.StatusInternalServerError, body)
		}()
		next.ServeHTTP(ww, r)
	})
}
