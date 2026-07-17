package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"hamster/internal/control"
	"hamster/internal/hook"
	"hamster/internal/ir"
	"hamster/internal/protocol"
	hamsterruntime "hamster/internal/runtime"
	"hamster/internal/store"
)

type Paths struct {
	Root     string
	Database string
	Socket   string
}

func DefaultPaths(home string) Paths {
	root := filepath.Join(home, ".hamster")
	return Paths{Root: root, Database: filepath.Join(root, "hamster.db"), Socket: filepath.Join(root, "run", "hamster.sock")}
}

type Server struct {
	store  *store.Store
	locks  sync.Map
	runner *hamsterruntime.Runner
}

func New(state *store.Store) *Server { return &Server{store: state, runner: hamsterruntime.New(state)} }

func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if err := removeStaleSocket(socketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("secure socket: %w", err)
	}

	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleConnection(ctx, connection)
	}
}

func (s *Server) handleConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	for {
		var request protocol.Request
		if err := protocol.Read(reader, &request); err != nil {
			return
		}
		response := s.Handle(ctx, request)
		if err := protocol.Write(connection, response); err != nil {
			return
		}
	}
}

type CreateRunRequest struct {
	WorkspaceRoot  string     `json:"workspace_root"`
	CodexSessionID string     `json:"codex_session_id"`
	AgentID        string     `json:"agent_id,omitempty"`
	Machine        ir.Machine `json:"machine"`
}

func (s *Server) Handle(ctx context.Context, request protocol.Request) protocol.Response {
	response := protocol.Response{Version: protocol.Version, RequestID: request.RequestID}
	if request.Version != protocol.Version {
		response.Error = &protocol.Error{Code: "protocol_version", Message: fmt.Sprintf("unsupported protocol version %d", request.Version)}
		return response
	}
	var result any
	var err error
	switch request.Method {
	case "ping":
		result = map[string]any{"ok": true}
	case "run.create":
		var params CreateRunRequest
		if err = json.Unmarshal(request.Params, &params); err == nil {
			var created *store.Run
			created, err = s.store.CreateRun(ctx, store.CreateRunParams{
				WorkspaceRoot: params.WorkspaceRoot, CodexSessionID: params.CodexSessionID,
				AgentID: params.AgentID, Machine: params.Machine,
			})
			if err == nil {
				result, err = s.runner.Start(ctx, created.ID)
			}
		}
	case "run.get":
		var params struct {
			RunID string `json:"run_id"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.GetRun(ctx, params.RunID)
		}
	case "run.current":
		var params struct {
			SessionID string `json:"session_id"`
			AgentID   string `json:"agent_id,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.FindCurrentRun(ctx, params.SessionID, params.AgentID)
		}
	case "hook.event":
		var params hook.HookRequest
		if err = json.Unmarshal(request.Params, &params); err == nil {
			var command control.Command
			var isControl bool
			if params.Event.ToolName == "Bash" {
				command, isControl, err = control.Parse(params.Event.Command())
			}
			if err != nil {
				break
			}
			if isControl && params.Event.EventName == "PreToolUse" {
				result, err = control.RegisterFromHook(ctx, s.store, s.runner, params.Event, command)
			} else if isControl {
				result = hook.Decision{Permission: "allow"}
			} else if params.Event.EventName == "PostToolUse" {
				result, err = s.runner.HandleHook(ctx, params.Event)
			} else {
				result, err = hook.NewEngine(s.store).Decide(ctx, params.Event)
			}
		}
	default:
		response.Error = &protocol.Error{Code: "method_not_found", Message: fmt.Sprintf("unknown method %q", request.Method)}
		return response
	}
	if err != nil {
		response.Error = &protocol.Error{Code: "request_failed", Message: err.Error()}
		return response
	}
	response.Result, err = json.Marshal(result)
	if err != nil {
		response.Error = &protocol.Error{Code: "internal", Message: err.Error()}
	}
	return response
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket path %s", path)
	}
	connection, dialErr := net.Dial("unix", path)
	if dialErr == nil {
		connection.Close()
		return fmt.Errorf("daemon is already listening on %s", path)
	}
	return os.Remove(path)
}
