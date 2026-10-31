package course

import (
	"log/slog"
	"mime"
	"net/http"
)

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
