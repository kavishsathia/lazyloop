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
	StateNested    StateKind = "nested"
	StateVerify    StateKind = "verify"
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
	ID         string        `json:"id"`
	Kind       StateKind     `json:"kind"`
	Executor   Executor      `json:"executor"`
	Name       string        `json:"name,omitempty"`
	Expression *Expression   `json:"expression,omitempty"`
	Nested     *Machine      `json:"nested,omitempty"`
	Limit      *Limit        `json:"limit,omitempty"`
	Policy     *Policy       `json:"policy,omitempty"`
	Next       string        `json:"next,omitempty"`
	OnSuccess  string        `json:"on_success,omitempty"`
	OnFailure  string        `json:"on_failure,omitempty"`
	Span       language.Span `json:"span"`
}

type Limit struct {
	Kind  string     `json:"kind"`
	Value Expression `json:"value"`
}

type Policy struct {
	Allow bool       `json:"allow"`
	Value Expression `json:"value"`
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
