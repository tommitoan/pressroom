package middleware

import (
	"encoding/json"
	"net/http"
)

// WriteError writes the service's JSON error body. Messages must never
// contain request content.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message, "code": code})
}
