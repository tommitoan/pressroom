package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover turns a panic in a handler into a generic 500 response. The panic
// value is logged without the request, because it could echo request content.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					logger.Error("panic serving request", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
					WriteError(w, http.StatusInternalServerError, "internal_error", "unexpected server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
