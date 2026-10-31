package course

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type errorResponse struct {
	Error string `json:"error"`
}

// maxBodyBytes caps a request body so a client cannot make the server buffer arbitrary input.
const maxBodyBytes = 64 << 10

// NewHandler builds the router. Cross-cutting concerns are middleware; each route has one method.
func NewHandler(service *Service) http.Handler {
	h := &Handler{service: service}

	r := chi.NewRouter()
	r.Use(recoverer)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
	})

	r.Get("/healthz", h.health)
	r.With(requireJSON, limitBody(maxBodyBytes)).Post("/attempts", h.recordAttempt)
	r.Get("/learners/{learner_id}/courses/{course_id}/progress", h.progress)

	return r
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSON(
			w,
			http.StatusRequestEntityTooLarge,
			errorResponse{Error: "request body is too large"},
		)
		return
	}

	writeJSON(
		w,
		http.StatusBadRequest,
		errorResponse{Error: "provide one valid JSON object with known fields"},
	)
}

func writeServiceError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "internal error"

	switch {
	case errors.Is(err, ErrNotImplemented):
		status, message = http.StatusNotImplemented, "candidate implementation required in internal/course/service.go"
	case errors.Is(err, ErrInvalidInput):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrUnknownCourse), errors.Is(err, ErrUnknownLesson):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, ErrConflict):
		status, message = http.StatusConflict, err.Error()
	default:
		slog.Error("application request failed", "error", err)
	}

	writeJSON(w, status, errorResponse{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("response encoding failed", "error", err)
	}
}
