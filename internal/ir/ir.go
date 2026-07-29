package ir

import "hamster/internal/language"

const Version = "hamster.ir/v1alpha1"

type Program struct {
	Version  string    `json:"version"`
	Machines []Machine `json:"machines"`
}

type LoopKind string

const (
	LoopGoal    LoopKind = "goal"
	LoopFlow    LoopKind = "flow"
	LoopForeach LoopKind = "foreach"
)

type Machine struct {
	ID           string        `json:"id"`
	Name         string        `json:"name,omitempty"`
	Kind         LoopKind      `json:"kind"`
	InitialState string        `json:"initial_state"`
	States       []State       `json:"states"`
	Return       *Expression   `json:"return,omitempty"`
	Foreach      *Foreach      `json:"foreach,omitempty"`
	Imperative   bool          `json:"imperative,omitempty"`
	Span         language.Span `json:"span"`
}

type Foreach struct {
	Binding  string     `json:"binding"`
	Sequence Expression `json:"sequence"`
	Body     *Machine   `json:"body"`
}

type StateKind string

const (
	StateAgentWork StateKind = "agent_work"
	StateStep      StateKind = "step"
	StateFork      StateKind = "fork"
	StateBranch    StateKind = "branch"
	StateExec      StateKind = "exec"
	StateWait      StateKind = "wait"
	StateNested    StateKind = "nested"
	StateVerify    StateKind = "verify"
	StateTrap      StateKind = "trap"
	StateJudge     StateKind = "judge"
	StateVerdict   StateKind = "verdict"
	StateLimit     StateKind = "limit"
	StatePolicy    StateKind = "policy"
	StateSequence  StateKind = "sequence"
	StateElement   StateKind = "element"
	StateBoundary  StateKind = "boundary"
	StateComplete  StateKind = "complete"
)

type Executor string

const (
	ExecutorAgent   Executor = "agent"
	ExecutorRuntime Executor = "runtime"
	ExecutorControl Executor = "control"
)

type State struct {
	ID         string      `json:"id"`
	Kind       StateKind   `json:"kind"`
	Executor   Executor    `json:"executor"`
	Name       string      `json:"name,omitempty"`
	Script     string      `json:"script,omitempty"`
	Expression *Expression `json:"expression,omitempty"`
	Nested     *Machine    `json:"nested,omitempty"`
	Limit      *Limit      `json:"limit,omitempty"`
	Policy     *Policy     `json:"policy,omitempty"`
	Trap       *Trap       `json:"trap,omitempty"`
	Judge      *Judge      `json:"judge,omitempty"`
	// Machine carries an exec target the compiler synthesized, so an observer
	// does not have to exist as a script on disk.
	Machine  *Machine `json:"machine,omitempty"`
	JudgeRef string   `json:"judge_ref,omitempty"`
	// Conditional marks a gate that sits on a branch rather than on every path.
	// Not reaching it is normal, so the iteration boundary must not read its
	// absence as a failure.
	Conditional bool          `json:"conditional,omitempty"`
	Next        string        `json:"next,omitempty"`
	OnSuccess   string        `json:"on_success,omitempty"`
	OnFailure   string        `json:"on_failure,omitempty"`
	Span        language.Span `json:"span"`
}

type Limit struct {
	Kind  string     `json:"kind"`
	Value Expression `json:"value"`
}

type Policy struct {
	Allow bool       `json:"allow"`
	Value Expression `json:"value"`
}

const (
	TrapApproval  = "approval"
	TrapSelection = "selection"
)

type Trap struct {
	Kind    string   `json:"kind"`
	Prompt  string   `json:"prompt"`
	Options []string `json:"options,omitempty"`
	// Clause names the taste this trap is standing in for. Resolving it writes
	// the supervisor's reasoning back as a proposed refinement.
	Clause string `json:"clause,omitempty"`
}

// Judge names a ratified taste clause and the evidence an observer is allowed
// to see. Evidence is gathered by the parent, never by the observer, so the
// observer cannot choose what to look at.
type Judge struct {
	// Prompt is what the observer is asked to validate. It applies the whole
	// ratified profile rather than one named clause, and investigates the
	// workspace itself instead of being handed a fixed set of evidence.
	Prompt   string `json:"prompt"`
	Escalate string `json:"escalate,omitempty"`
	// EscalateAfter bounds how many times the same clause may fail before the
	// question goes to a human. Not every taste failure is one the agent can
	// perceive, so looping forever wastes attempts on work it cannot fix.
	EscalateAfter int `json:"escalate_after,omitempty"`
	// AuditRate samples one in N passing verdicts for human spot-checking. It
	// is how a rubric that has gone hollow becomes visible.
	AuditRate int `json:"audit_rate,omitempty"`
}

type ExpressionKind string

const (
	ExpressionIdentifier ExpressionKind = "identifier"
	ExpressionLiteral    ExpressionKind = "literal"
	ExpressionMember     ExpressionKind = "member"
	ExpressionCall       ExpressionKind = "call"
	ExpressionBinary     ExpressionKind = "binary"
	ExpressionUnary      ExpressionKind = "unary"
	ExpressionList       ExpressionKind = "list"
)

type Expression struct {
	Kind      ExpressionKind `json:"kind"`
	Name      string         `json:"name,omitempty"`
	Value     string         `json:"value,omitempty"`
	ValueKind string         `json:"value_kind,omitempty"`
	Operator  string         `json:"operator,omitempty"`
	Receiver  *Expression    `json:"receiver,omitempty"`
	Callee    *Expression    `json:"callee,omitempty"`
	Left      *Expression    `json:"left,omitempty"`
	Right     *Expression    `json:"right,omitempty"`
	Operand   *Expression    `json:"operand,omitempty"`
	Arguments []Argument     `json:"arguments,omitempty"`
	Elements  []Expression   `json:"elements,omitempty"`
	Span      language.Span  `json:"span"`
}

type Argument struct {
	Name  string     `json:"name,omitempty"`
	Value Expression `json:"value"`
}
