package course

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type attemptRequest struct {
	AttemptID string `json:"attempt_id"`
	LearnerID string `json:"learner_id"`
	CourseID  string `json:"course_id"`
	LessonID  string `json:"lesson_id"`
	Score     *int   `json:"score"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// maxBodyBytes caps a request body so a client cannot make the server buffer arbitrary input.
const maxBodyBytes = 64 << 10

// Handler serves the course HTTP API on top of a Service.
type Handler struct {
	service *Service
}

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

// recoverer turns a panic in any handler into a logged JSON 500 instead of a dropped connection.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}

			// net/http uses this value to abort a response on purpose; it must keep propagating.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}

			slog.Error("panic while handling request", "panic", rec, "method", r.Method, "path", r.URL.Path)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		}()

		next.ServeHTTP(w, r)
	})
}

// requireJSON rejects requests whose Content-Type is not application/json.
func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeJSON(w, http.StatusUnsupportedMediaType, errorResponse{Error: "use application/json"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// limitBody makes reads past max bytes fail with *http.MaxBytesError.
func limitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) recordAttempt(w http.ResponseWriter, r *http.Request) {
	attempt, ok := decodeAttempt(w, r)
	if !ok {
		return
	}

	if err := h.service.RecordAttempt(r.Context(), attempt); err != nil {
		writeServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, attempt)
}

func (h *Handler) progress(w http.ResponseWriter, r *http.Request) {
	progress, err := h.service.Progress(r.Context(), chi.URLParam(r, "learner_id"), chi.URLParam(r, "course_id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, progress)
}

// decodeAttempt reads exactly one JSON object. On failure it writes the response and returns false.
func decodeAttempt(w http.ResponseWriter, r *http.Request) (Attempt, bool) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var input attemptRequest
	if err := decoder.Decode(&input); err != nil {
		writeDecodeError(w, err)
		return Attempt{}, false
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeDecodeError(w, err)
		return Attempt{}, false
	}

	if input.Score == nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "score is required and must be an integer"})
		return Attempt{}, false
	}

	return Attempt{
		AttemptID: input.AttemptID,
		LearnerID: input.LearnerID,
		CourseID:  input.CourseID,
		LessonID:  input.LessonID,
		Score:     *input.Score,
	}, true
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
