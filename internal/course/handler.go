package course

import (
	"encoding/json"
	"io"
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

// Handler serves the course HTTP API on top of a Service.
type Handler struct {
	service *Service
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
