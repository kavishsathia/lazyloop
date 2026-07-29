package playground

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hamster/internal/control"
	"hamster/internal/store"
)

//go:embed web/*
var embedded embed.FS

// Handler serves the playground. It is a separate utility from the
// observability console: that one watches a session you are already running,
// this one starts sessions so you can see how a loop behaves before you trust
// it with real work.
type Handler struct {
	store      *store.Store
	supervisor *Supervisor
	static     http.Handler
}

func NewHandler(state *store.Store, supervisor *Supervisor) http.Handler {
	assets, err := fs.Sub(embedded, "web")
	if err != nil {
		panic(err)
	}
	return securityHeaders(&Handler{store: state, supervisor: supervisor, static: http.FileServer(http.FS(assets))})
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	switch {
	case path == "/api/compile" && request.Method == http.MethodPost:
		h.compile(writer, request)
	case path == "/api/runs" && request.Method == http.MethodPost:
		h.launch(writer, request)
	case path == "/api/runs" && request.Method == http.MethodGet:
		h.list(writer, request)
	case strings.HasPrefix(path, "/api/runs/") && strings.HasSuffix(path, "/cancel") && request.Method == http.MethodPost:
		h.cancel(writer, strings.TrimSuffix(strings.TrimPrefix(path, "/api/runs/"), "/cancel"))
	case strings.HasPrefix(path, "/api/runs/") && request.Method == http.MethodGet:
		h.detail(writer, request, strings.TrimPrefix(path, "/api/runs/"))
	case request.Method != http.MethodGet && request.Method != http.MethodHead:
		writer.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	case path == "/" || path == "/index.html" || path == "/app.css" || path == "/app.js":
		writer.Header().Set("Cache-Control", "no-store")
		h.static.ServeHTTP(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

// compile reports diagnostics without running anything, so a broken loop is a
// message next to the editor rather than a failed experiment.
func (h *Handler) compile(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Script string `json:"script"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20)).Decode(&body); err != nil {
		h.writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	machine, err := control.CompileSource(body.Script)
	if err != nil {
		h.writeJSON(writer, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	kinds := map[string]int{}
	for _, state := range machine.States {
		kinds[string(state.Kind)]++
	}
	h.writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "states": len(machine.States), "kinds": kinds})
}

func (h *Handler) launch(writer http.ResponseWriter, request *http.Request) {
	var params LaunchParams
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20)).Decode(&params); err != nil {
		h.writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	run, err := h.supervisor.Launch(request.Context(), params)
	if err != nil {
		h.writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	h.writeJSON(writer, http.StatusOK, run)
}

func (h *Handler) list(writer http.ResponseWriter, request *http.Request) {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	runs, err := h.store.ListPlaygroundRuns(request.Context(), limit)
	if err != nil {
		h.writeError(writer, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, map[string]any{"runs": runs})
}

func (h *Handler) cancel(writer http.ResponseWriter, runID string) {
	h.writeJSON(writer, http.StatusOK, map[string]any{"cancelled": h.supervisor.Cancel(runID)})
}

// detail merges what the agent did with what Hamster decided about it. Seeing
// them apart tells you little; seeing a denial next to the tool call it refused
// is the whole point of the playground.
func (h *Handler) detail(writer http.ResponseWriter, request *http.Request, runID string) {
	if runID == "" || strings.ContainsRune(runID, '/') {
		http.NotFound(writer, request)
		return
	}
	run, err := h.store.GetPlaygroundRun(request.Context(), runID)
	if err != nil {
		h.writeError(writer, err)
		return
	}
	after, _ := strconv.Atoi(request.URL.Query().Get("after"))
	events, err := h.store.PlaygroundEventsAfter(request.Context(), runID, after)
	if err != nil {
		h.writeError(writer, err)
		return
	}
	decisions, err := h.store.PlaygroundDecisions(request.Context(), run.WorkspaceRoot)
	if err != nil {
		h.writeError(writer, err)
		return
	}
	loops, err := h.store.PlaygroundRunsForWorkspace(request.Context(), run.WorkspaceRoot)
	if err != nil {
		h.writeError(writer, err)
		return
	}
	traps, err := h.store.ListOpenTraps(request.Context(), run.WorkspaceRoot)
	if err != nil {
		h.writeError(writer, err)
		return
	}
	h.writeJSON(writer, http.StatusOK, map[string]any{
		"run": run, "events": events, "decisions": decisions,
		"loops": summarize(loops), "open_traps": traps, "generated_at": time.Now().UTC(),
	})
}

type loopSummary struct {
	ID        string          `json:"id"`
	Machine   string          `json:"machine"`
	State     string          `json:"state"`
	Status    string          `json:"status"`
	Iteration int             `json:"iteration"`
	Result    json.RawMessage `json:"result,omitempty"`
	Reason    string          `json:"termination_reason,omitempty"`
}

func summarize(runs []store.Run) []loopSummary {
	summaries := make([]loopSummary, 0, len(runs))
	for _, run := range runs {
		name := run.Machine.Name
		if name == "" {
			name = "anonymous " + string(run.Machine.Kind)
		}
		summaries = append(summaries, loopSummary{
			ID: run.ID, Machine: name, State: run.StateID, Status: run.Status,
			Iteration: run.Iteration, Result: run.Result, Reason: run.TerminationReason,
		})
	}
	return summaries
}

func (h *Handler) writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func (h *Handler) writeError(writer http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		h.writeJSON(writer, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	h.writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

// The playground starts processes, so unlike the read-only console it must never
// be reachable from anywhere but this machine.
func ValidateAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("the playground launches agents and must listen on a loopback address only")
	}
	return nil
}
