package api

import (
	"net/http"
	"strconv"

	"github.com/tommitoan/pressroom/internal/middleware"
)

// apiError is an error the API reports to its caller. Messages are fixed text:
// they never include any part of the request.
type apiError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter int // seconds; zero omits the header
}

func (e *apiError) write(w http.ResponseWriter) {
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfter))
	}
	middleware.WriteError(w, e.Status, e.Code, e.Message)
}

func errInvalidJSON() *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: "invalid_json", Message: "request body must be one JSON object with known fields"}
}

func errPayloadTooLarge() *apiError {
	return &apiError{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", Message: "request body is too large"}
}

func invalidOptions(message string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: "invalid_options", Message: message}
}
