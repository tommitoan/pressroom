package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuth rejects requests that do not carry the expected bearer token.
// Both values are hashed first so the comparison takes the same time whatever
// their lengths.
func BearerAuth(token string) func(http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, value, found := strings.Cut(r.Header.Get("Authorization"), " ")
			got := sha256.Sum256([]byte(strings.TrimSpace(value)))
			if !found || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				WriteError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
