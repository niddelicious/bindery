package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/httpsec"
)

// upstreamDetailMax caps the upstream text a client may see, in bytes.
const upstreamDetailMax = 200

// writeNotFoundOrServerError answers a lookup that returned (value, err):
// a non-nil err is a server fault (generic 500, the error is logged), and
// found == false with no error is a plain 404. Conflating the two, as the
// older "err != nil || x == nil" shape does, reports a database outage as a
// missing record.
func writeNotFoundOrServerError(w http.ResponseWriter, r *http.Request, err error, found bool) {
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// decodeJSON decodes the request body into v and reports whether the handler
// may continue. A body past the server's size cap answers 413; any other
// decode failure answers 400 with a fixed message, so the decoder's own text
// (which can quote the payload) never reaches the client. On false the
// response is already written.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil {
		return true
	}
	writeBodyReadError(w, err)
	return false
}

// writeBodyReadError answers a failure to read or decode a request body:
// 413 when the body ran past the cap installed by MaxRequestBody or
// WithMaxBody (http.MaxBytesReader reports *http.MaxBytesError), otherwise a
// 400 with a fixed message. Handlers that buffer the body themselves
// (io.ReadAll) use this directly; everything else goes through decodeJSON.
func writeBodyReadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
}

// NotFoundJSON answers a request that matched no route on the API
// sub-routers with the standard JSON error shape, where chi's default is the
// plain text "404 page not found". It is installed with r.NotFound inside the
// /api and /api/v1 Route closures only, so the SPA fallback and the /opds,
// /auth and /bridge mounts keep their own behaviour.
func NotFoundJSON(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

// allowProbeMethods are the methods MethodNotAllowedJSON tests a path with.
var allowProbeMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

// MethodNotAllowedJSON returns the 405 handler for a chi router: the standard
// JSON error shape plus the Allow header RFC 9110 requires. chi only fills in
// Allow for its own default handler (the list is not passed to custom ones),
// so the allowed methods are found by asking routes, the router the handler is
// installed on, which of the usual methods match the path.
func MethodNotAllowedJSON(routes chi.Routes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePath != "" {
			path = rctx.RoutePath // relative to the sub-router, as its tree expects
		}
		var allowed []string
		for _, m := range allowProbeMethods {
			if routes.Match(chi.NewRouteContext(), m, path) {
				allowed = append(allowed, m)
			}
		}
		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
		}
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// Free text credentials. httpsec.RedactSecrets only knows URL shaped secrets
// (?apikey=..., user:pass@host), but a service's own error line can quote a
// header or a form field: "Authorization: Bearer x", "apikey=x", {"token":"x"}.
var (
	// A credential header: the rest of the line goes.
	secretHeaderRE = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|x-api-key|set-cookie|cookie)\s*:[^\r\n]*`)
	// A credential field written name=value or "name":"value". A bare
	// "name: word" is left alone, since prose such as "token: expired" is the
	// useful part of a message.
	secretFieldRE = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|token|password|passwd|secret)\b((?:["']\s*:\s*["']?)|(?:\s*=\s*["']?))[^\s"',;&<>]+`)
)

// redactFreeText removes credentials that RedactSecrets cannot see because
// they are not part of a URL.
func redactFreeText(s string) string {
	s = secretHeaderRE.ReplaceAllString(s, "${1}: REDACTED")
	return secretFieldRE.ReplaceAllString(s, "${1}${2}REDACTED")
}

// upstreamDetail reduces text that came from another service (a download
// client, Prowlarr) to something safe to show: credentials redacted, the first
// line only, at most upstreamDetailMax bytes. Upstream bodies can be HTML
// error pages or stack traces, and a first line is enough for an operator to
// see what the service objected to.
func upstreamDetail(msg string) string {
	// Trim first, so a body that opens with a blank line still yields its
	// first line of text rather than nothing.
	msg = strings.TrimSpace(redactFreeText(httpsec.RedactSecrets(msg)))
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	if len(msg) > upstreamDetailMax {
		// Cut on a rune boundary so a multi-byte character is never split.
		cut := upstreamDetailMax
		for cut > 0 && msg[cut]&0xC0 == 0x80 {
			cut--
		}
		msg = msg[:cut]
	}
	if msg == "" {
		return upstreamDetailEmpty
	}
	return msg
}

// upstreamDetailEmpty is what a caller sees when the other service sent no
// text at all, so the UI never renders a blank reason.
const upstreamDetailEmpty = "the service returned an error with no message"

// writeUpstreamDetail is for connection-test style endpoints, where the
// operator needs some of what the other service said. err is logged in full;
// the client gets the first line of msg (see upstreamDetail). Endpoints that
// do not need the detail use writeUpstreamError instead.
func writeUpstreamDetail(w http.ResponseWriter, r *http.Request, status int, msg string, err error) {
	slog.Warn("upstream request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeJSON(w, status, map[string]string{"error": upstreamDetail(msg)})
}
