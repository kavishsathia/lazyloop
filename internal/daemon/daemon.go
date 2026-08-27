package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hamster/internal/control"
	"hamster/internal/hook"
	"hamster/internal/ir"
	"hamster/internal/observe"
	"hamster/internal/protocol"
	hamsterruntime "hamster/internal/runtime"
	"hamster/internal/store"
	"hamster/internal/trace"
)

type Paths struct {
	Root             string
	Database         string
	Socket           string
	DashboardAddress string
	DashboardFile    string
}

func DefaultPaths(home string) Paths {
	root := filepath.Join(home, ".hamster")
	return Paths{
		Root: root, Database: filepath.Join(root, "hamster.db"), Socket: filepath.Join(root, "run", "hamster.sock"),
		DashboardAddress: "127.0.0.1:7331", DashboardFile: filepath.Join(root, "run", "dashboard-url"),
	}
}

type Server struct {
	store    *store.Store
	locks    sync.Map
	runner   *hamsterruntime.Runner
	identity Identity
}

func New(state *store.Store) *Server {
	runner := hamsterruntime.New(state)
	runner.SetExecResolver(control.ResolveScriptMachine)
	return &Server{store: state, runner: runner, identity: CurrentIdentity()}
}

func (s *Server) Serve(ctx context.Context, socketPath string) error {
	return s.serve(ctx, socketPath, "", "")
}

func (s *Server) ServeWithDashboard(ctx context.Context, socketPath, dashboardAddress, dashboardFile string) error {
	return s.serve(ctx, socketPath, dashboardAddress, dashboardFile)
}

func (s *Server) serve(ctx context.Context, socketPath, dashboardAddress, dashboardFile string) error {
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
	var dashboardListener net.Listener
	var dashboardServer *http.Server
	if dashboardAddress != "" {
		if err := validateDashboardAddress(dashboardAddress); err != nil {
			return err
		}
		dashboardListener, err = net.Listen("tcp", dashboardAddress)
		if err != nil {
			// The console is a convenience; the socket is the daemon's job. A
			// busy port must not stop the daemon, because a hook that cannot
			// reach one exits non-zero and takes the agent's whole turn with
			// it — which is how a second Hamster home used to break silently.
			dashboardListener, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return fmt.Errorf("listen on dashboard %s: %w", dashboardAddress, err)
			}
		}
		dashboardServer = &http.Server{Handler: observe.NewHandler(s.store), ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = dashboardServer.Serve(dashboardListener) }()
		if dashboardFile != "" {
			if err := os.MkdirAll(filepath.Dir(dashboardFile), 0o700); err != nil {
				dashboardListener.Close()
				return err
			}
			if err := os.WriteFile(dashboardFile, []byte("http://"+dashboardListener.Addr().String()+"\n"), 0o600); err != nil {
				dashboardListener.Close()
				return err
			}
			defer os.Remove(dashboardFile)
		}
		defer dashboardListener.Close()
	}

	go func() {
		<-ctx.Done()
		listener.Close()
		if dashboardServer != nil {
			_ = dashboardServer.Close()
		}
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

func validateDashboardAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid dashboard address %q: %w", address, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("dashboard must listen on a loopback address, got %q", address)
	}
	return nil
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
		result = map[string]any{"ok": true, "identity": s.identity}
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
	case "run.kill":
		var params struct {
			SessionID string `json:"session_id"`
			AgentID   string `json:"agent_id,omitempty"`
			Reason    string `json:"reason,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.KillCurrentRun(ctx, params.SessionID, params.AgentID, params.Reason)
		}
	case "process.fork":
		var params struct {
			ParentRunID  string `json:"parent_run_id"`
			ChildAgentID string `json:"child_agent_id"`
			ForkKey      string `json:"fork_key"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.ForkRun(ctx, params.ParentRunID, params.ChildAgentID, params.ForkKey)
		}
	case "process.exec":
		var params struct {
			RunID     string          `json:"run_id"`
			Machine   ir.Machine      `json:"machine"`
			Variables json.RawMessage `json:"variables,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			var executed *store.Run
			executed, err = s.store.ExecRun(ctx, params.RunID, params.Machine, params.Variables)
			if err == nil {
				result, err = s.runner.Start(ctx, executed.ID)
			}
		}
	case "process.wait":
		var params struct {
			ParentRunID string `json:"parent_run_id"`
			ChildRunID  string `json:"child_run_id"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.WaitRun(ctx, params.ParentRunID, params.ChildRunID)
		}
	case "process.return":
		var params struct {
			RunID string          `json:"run_id"`
			Value json.RawMessage `json:"value"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.ReturnRun(ctx, params.RunID, params.Value)
		}
	case "trap.list":
		var params struct {
			WorkspaceRoot string `json:"workspace_root,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.ListOpenTraps(ctx, params.WorkspaceRoot)
		}
	case "trap.resolve":
		var params struct {
			TrapID     string `json:"trap_id"`
			Resolution string `json:"resolution"`
			Note       string `json:"note,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			var resolved *store.Trap
			resolved, err = s.store.ResolveTrap(ctx, params.TrapID, params.Resolution, params.Note)
			if err == nil {
				// Resolving is what makes the run runnable again, so the
				// daemon immediately pumps it rather than waiting for the
				// agent's next tool call.
				if _, startErr := s.runner.Start(ctx, resolved.RunID); startErr != nil {
					err = startErr
				} else {
					result, err = s.store.GetTrap(ctx, resolved.ID)
				}
			}
		}
	case "audit.list":
		var params struct {
			WorkspaceRoot string `json:"workspace_root,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.ListAudits(ctx, params.WorkspaceRoot)
		}
	case "audit.resolve":
		var params struct {
			JudgementID string `json:"judgement_id"`
			Outcome     string `json:"outcome"`
			Note        string `json:"note,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.ResolveAudit(ctx, params.JudgementID, params.Outcome, params.Note)
		}
	case "taste.record":
		var params struct {
			WorkspaceRoot string `json:"workspace_root"`
			Name          string `json:"name"`
			Question      string `json:"question,omitempty"`
			Clause        string `json:"clause"`
			Source        string `json:"source,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.RecordTaste(ctx, store.RecordTasteParams{
				WorkspaceRoot: params.WorkspaceRoot, Name: params.Name,
				Question: params.Question, Clause: params.Clause, Source: params.Source,
			})
		}
	case "taste.ratify":
		var params struct {
			WorkspaceRoot string `json:"workspace_root"`
			Name          string `json:"name,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.RatifyTaste(ctx, params.WorkspaceRoot, params.Name)
		}
	case "taste.revoke":
		var params struct {
			WorkspaceRoot string `json:"workspace_root"`
			Name          string `json:"name"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			if err = s.store.RevokeTaste(ctx, params.WorkspaceRoot, params.Name); err == nil {
				result = map[string]any{"revoked": params.Name}
			}
		}
	case "taste.list":
		var params struct {
			WorkspaceRoot string `json:"workspace_root,omitempty"`
			RatifiedOnly  bool   `json:"ratified_only"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.ListTaste(ctx, params.WorkspaceRoot, params.RatifiedOnly)
		}
	case "trace.start":
		var params struct {
			Name              string `json:"name"`
			CodexDistribution string `json:"codex_distribution"`
			CodexVersion      string `json:"codex_version"`
			SessionFilter     string `json:"session_filter,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.StartTrace(ctx, params.Name, params.CodexDistribution, params.CodexVersion, params.SessionFilter)
		}
	case "trace.stop":
		var params struct {
			TraceID string `json:"trace_id,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.StopTrace(ctx, params.TraceID)
		}
	case "trace.get":
		var params struct {
			TraceID string `json:"trace_id"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.GetTraceData(ctx, params.TraceID)
		}
	case "console.observe":
		var params struct {
			Console string `json:"console"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.ObserveConsole(ctx, params.Console)
		}
	case "console.arm":
		var params struct {
			Console       string     `json:"console"`
			WorkspaceRoot string     `json:"workspace_root"`
			Machine       ir.Machine `json:"machine"`
			Source        string     `json:"source,omitempty"`
			Label         string     `json:"label,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.ArmLoop(ctx, params.Console, params.WorkspaceRoot, params.Machine, params.Source, params.Label)
		}
	case "console.disarm":
		var params struct {
			Console string `json:"console"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			err = s.store.DisarmLoop(ctx, params.Console)
			result = map[string]any{"ok": err == nil}
		}
	case "loops.list":
		var params struct {
			WorkspaceRoot string `json:"workspace_root,omitempty"`
			Limit         int    `json:"limit,omitempty"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.ListLoops(ctx, params.WorkspaceRoot, params.Limit)
		}
	case "loops.get":
		var params struct {
			ID string `json:"id"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			result, err = s.store.LoadLoop(ctx, params.ID)
		}
	case "loops.stop":
		var params struct {
			ID string `json:"id"`
		}
		if err = json.Unmarshal(request.Params, &params); err == nil {
			err = s.store.StopLoop(ctx, params.ID)
			result = map[string]any{"ok": err == nil}
		}
	case "hook.event":
		var params hook.HookRequest
		if err = json.Unmarshal(request.Params, &params); err == nil {
			var boundChild *store.Run
			if params.Event.AgentID != "" {
				boundChild, err = s.store.FindCurrentRun(ctx, params.Event.SessionID, params.Event.AgentID)
				if err == sql.ErrNoRows {
					boundChild, err = s.store.ClaimPendingFork(ctx, params.Event.SessionID, params.Event.AgentID)
					if err == nil {
						boundChild, err = s.runner.ResumeForkChild(ctx, boundChild.ID)
					} else if err == sql.ErrNoRows {
						err = nil
					}
				}
			}
			if err != nil {
				break
			}
			var launched *store.Run
			if params.Console != "" {
				launched, err = s.bindConsole(ctx, params)
				if err != nil {
					break
				}
			}
			before := s.traceSnapshot(ctx, params.Event)
			var command control.Command
			var isControl bool
			if params.Event.ToolName == "Bash" {
				command, isControl, err = control.Parse(params.Event.Command())
			}
			if err != nil {
				break
			}
			if params.Event.EventName == "SubagentStart" {
				if boundChild != nil {
					result = hook.Decision{Permission: "allow", Tracked: true, RunID: boundChild.ID, Context: fmt.Sprintf("Hamster automatically bound Codex agent %s to forked run %s.", params.Event.AgentID, boundChild.ID)}
				} else {
					result = hook.Decision{Permission: "allow", Context: "No pending Hamster fork is associated with this subagent."}
				}
			} else if isControl && params.Event.EventName == "PreToolUse" {
				result, err = control.RegisterFromHook(ctx, s.store, s.runner, params.Event, command)
			} else if isControl {
				result = hook.Decision{Permission: "allow"}
			} else if params.Event.EventName == "PreToolUse" && control.Invokes(params.Event.Command()) {
				// Parse only recognizes a Hamster command that is the whole tool
				// call. Anything else that still invokes the CLI would reach the
				// daemon without ever passing the authority check.
				result = control.DenyDisguisedInvocation()
			} else if params.Event.EventName == "Stop" {
				result, err = s.runner.HandleStop(ctx, params.Event)
			} else if params.Event.EventName == "SubagentStop" {
				result, err = s.runner.HandleSubagentStop(ctx, params.Event)
			} else if params.Event.EventName == "UserPromptSubmit" {
				result, err = s.runner.HandleUserPrompt(ctx, params.Event)
			} else if params.Event.EventName == "PostToolUse" {
				if isSpawnTool(params.Event.ToolName) {
					var child *store.Run
					child, err = s.store.FindFork(ctx, forkKeyForEvent(params.Event))
					if err == nil {
						var response map[string]any
						if json.Unmarshal(params.Event.ToolResponse, &response) != nil || response == nil {
							response = map[string]any{}
						}
						response["hamster_child_run_id"] = child.ID
						params.Event.ToolResponse, _ = json.Marshal(response)
					}
				}
				if err != nil {
					break
				}
				result, err = s.runner.HandleHook(ctx, params.Event)
			} else {
				var decision hook.Decision
				decision, err = hook.NewEngine(s.store).Decide(ctx, params.Event)
				if err == nil && params.Event.EventName == "PreToolUse" && isSpawnTool(params.Event.ToolName) && decision.Permission == "allow" && decision.Tracked {
					var child *store.Run
					child, err = s.store.PrepareFork(ctx, decision.RunID, forkKey(params.Event.RequestID()))
					if err == nil {
						decision.Context = fmt.Sprintf("Hamster prepared forked run %s; the child's stable hook identity will be bound automatically.", child.ID)
					}
				}
				result = decision
			}
			if decision, ok := result.(hook.Decision); ok && err == nil && launched != nil {
				result = announceLaunch(decision, launched)
			}
			if err == nil {
				after := s.traceSnapshot(ctx, params.Event)
				input := params.Event.Raw
				if len(input) == 0 {
					input = hook.MarshalEvent(params.Event)
				}
				decision, _ := json.Marshal(result)
				beforeJSON, _ := json.Marshal(before)
				afterJSON, _ := json.Marshal(after)
				runID := ""
				if after != nil {
					runID = after.RunID
				} else if before != nil {
					runID = before.RunID
				}
				verificationJSON, _ := json.Marshal(s.runner.TakeObservations(runID))
				verifications := nullableVerifications(verificationJSON)
				if len(verifications) > 0 {
					err = s.store.SetEventVerifications(ctx, params.Event.RequestID(), verifications)
				}
				if err == nil {
					err = s.store.AppendActiveTrace(ctx, params.Event.SessionID, input, decision,
						nullableSnapshot(beforeJSON, before), nullableSnapshot(afterJSON, after), verifications)
				}
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

// bindConsole records which Codex session a lazyloop screen is watching and,
// when that screen has armed a loop and the root agent is not yet governed,
// claims it. Claiming happens as work begins — a prompt or a tool call — not
// as a turn ends, so the agent reads about the loop before doing anything
// under it.
func (s *Server) bindConsole(ctx context.Context, params hook.HookRequest) (*store.Run, error) {
	event := params.Event
	if event.SessionID == "" {
		return nil, nil
	}
	if err := s.store.BindConsole(ctx, params.Console, event.SessionID, event.WorkspaceRoot); err != nil {
		return nil, err
	}
	if event.AgentID != "" || (event.EventName != "UserPromptSubmit" && event.EventName != "PreToolUse") {
		return nil, nil
	}
	if _, err := s.store.FindCurrentRun(ctx, event.SessionID, ""); err == nil {
		return nil, nil
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	claimed, err := s.store.ClaimLoop(ctx, params.Console, event.SessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.runner.Start(ctx, claimed.ID)
}

func announceLaunch(decision hook.Decision, launched *store.Run) hook.Decision {
	if decision.Permission == "deny" {
		return decision
	}
	announcement := fmt.Sprintf("Hamster started run %s for %s at state %s from the lazyloop console.", launched.ID, control.MachineLabel(launched.Machine), launched.StateID)
	if commands := hamsterruntime.VerifierCommands(launched.Machine); len(commands) > 0 {
		quoted := make([]string, len(commands))
		for index, command := range commands {
			quoted[index] = "`" + command + "`"
		}
		announcement += fmt.Sprintf(" The loop completes when %s passes; Hamster runs it after every tool call and reports what it prints.", strings.Join(quoted, " and "))
	}
	announcement += " Follow this loop until Hamster reports completion or escalation."
	if decision.Context != "" {
		announcement += " " + decision.Context
	}
	decision.Context = announcement
	decision.Tracked = true
	if decision.RunID == "" {
		decision.RunID = launched.ID
	}
	return decision
}

func isSpawnTool(name string) bool {
	return strings.EqualFold(name, "collaborationspawn_agent") || strings.EqualFold(name, "spawn_agent") || strings.EqualFold(name, "Agent")
}

func forkKey(requestID string) string {
	digest := sha256.Sum256([]byte(requestID))
	return fmt.Sprintf("fork_%x", digest[:16])
}

func forkKeyForEvent(event hook.Event) string {
	event.EventName = "PreToolUse"
	return forkKey(event.RequestID())
}

func (s *Server) traceSnapshot(ctx context.Context, event hook.Event) *trace.StateSnapshot {
	run, err := s.store.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err != nil {
		return nil
	}
	return &trace.StateSnapshot{
		RunID: run.ID, ProcessID: run.ProcessID, MachineID: run.Machine.ID, MachineName: run.Machine.Name,
		MachineKind: string(run.Machine.Kind), StateID: run.StateID, Status: run.Status,
		Iteration: run.Iteration, StackDepth: len(run.Stack),
	}
}

func nullableSnapshot(payload []byte, snapshot *trace.StateSnapshot) json.RawMessage {
	if snapshot == nil {
		return nil
	}
	return payload
}

func nullableVerifications(payload []byte) json.RawMessage {
	if string(payload) == "null" || string(payload) == "[]" {
		return nil
	}
	return payload
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
