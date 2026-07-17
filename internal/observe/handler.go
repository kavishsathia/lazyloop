package observe

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"hamster/internal/store"
)

//go:embed web/*
var embedded embed.FS

type Handler struct {
	store  *store.Store
	static http.Handler
}

func NewHandler(state *store.Store) http.Handler {
	assets, err := fs.Sub(embedded, "web")
	if err != nil {
		panic(err)
	}
	return securityHeaders(&Handler{store: state, static: http.FileServer(http.FS(assets))})
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch {
	case request.URL.Path == "/api/v1/health":
		h.writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
	case request.URL.Path == "/api/v1/snapshot":
		sessions, err := h.store.ObserveSessions(request.Context())
		if err != nil {
			h.writeError(writer, err)
			return
		}
		h.writeJSON(writer, http.StatusOK, map[string]any{"generated_at": time.Now().UTC(), "sessions": sessions})
	case strings.HasPrefix(request.URL.Path, "/api/v1/runs/"):
		runID := strings.TrimPrefix(request.URL.Path, "/api/v1/runs/")
		if runID == "" || strings.ContainsRune(runID, '/') {
			http.NotFound(writer, request)
			return
		}
		detail, err := h.store.ObserveRun(request.Context(), runID)
		if err != nil {
			h.writeError(writer, err)
			return
		}
		h.writeJSON(writer, http.StatusOK, detail)
	case request.URL.Path == "/" || request.URL.Path == "/index.html":
		writer.Header().Set("Cache-Control", "no-store")
		h.static.ServeHTTP(writer, request)
	case request.URL.Path == "/app.css" || request.URL.Path == "/app.js":
		writer.Header().Set("Cache-Control", "public, max-age=300")
		h.static.ServeHTTP(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (h *Handler) writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func (h *Handler) writeError(writer http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		h.writeJSON(writer, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	h.writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "observability query failed"})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(writer, request)
	})
}
