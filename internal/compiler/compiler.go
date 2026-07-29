package compiler

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"hamster/internal/ir"
	"hamster/internal/language"
)

type compiler struct {
	nextMachine int
	diagnostics language.Diagnostics
}

func Compile(program *language.Program) (*ir.Program, language.Diagnostics) {
	c := &compiler{}
	result := &ir.Program{Version: ir.Version}
	for _, loop := range program.Loops {
		result.Machines = append(result.Machines, *c.compileLoop(loop))
	}
	if len(result.Machines) == 0 {
		c.error(language.Span{}, "program must contain at least one loop")
	} else if len(result.Machines) > 1 {
		c.error(program.Loops[1].NodeSpan(), "program must contain exactly one anonymous root loop")
	}
	return result, c.diagnostics
}

func (c *compiler) compileLoop(loop language.Loop) *ir.Machine {
	machine := &ir.Machine{
		ID: fmt.Sprintf("machine_%d", c.nextMachine), Span: loop.NodeSpan(),
	}
	c.nextMachine++

	switch node := loop.(type) {
	case *language.Goal:
		machine.Kind = ir.LoopGoal
		c.compileGoal(machine, node)
	case *language.Foreach:
		machine.Kind = ir.LoopForeach
		machine.Foreach = &ir.Foreach{
			Binding:  node.Binding,
			Sequence: c.compileExpression(node.Sequence),
			Body:     c.compileLoop(node.Body),
		}
		c.compileForeachStates(machine)
	default:
		panic(fmt.Sprintf("unsupported loop type %T", loop))
	}
	return machine
}

func (c *compiler) compileGoal(machine *ir.Machine, goal *language.Goal) {
	for _, statement := range goal.Statements {
		switch node := statement.(type) {
		case *language.Limit:
			c.validateLimit(node)
			value := c.compileExpression(node.Value)
			machine.States = append(machine.States, ir.State{Kind: ir.StateLimit, Executor: ir.ExecutorControl, Name: node.Kind, Limit: &ir.Limit{Kind: node.Kind, Value: value}, Span: node.Span})
		case *language.Policy:
			value := c.compileExpression(node.Value)
			machine.States = append(machine.States, ir.State{Kind: ir.StatePolicy, Executor: ir.ExecutorControl, Policy: &ir.Policy{Allow: node.Allow, Value: value}, Span: node.Span})
		}
	}
	if goal.Flow == nil {
		machine.States = append(machine.States, ir.State{Kind: ir.StateAgentWork, Executor: ir.ExecutorAgent, Name: "work", Span: machine.Span})
	} else {
		machine.Imperative = true
		if len(goal.Flow.Statements) == 0 {
			c.error(goal.Flow.Span, "attached flow must contain at least one statement")
		}
		c.compileFlowStatements(machine, goal.Flow.Statements)
	}
	gateCount := 0
	returnCount := 0
	for _, statement := range goal.Statements {
		switch node := statement.(type) {
		case *language.Verify:
			gateCount++
			expression := c.compileExpression(node.Check)
			machine.States = append(machine.States, ir.State{Kind: ir.StateVerify, Executor: ir.ExecutorRuntime, Name: node.Name, Expression: &expression, Span: node.Span})
		case *language.Return:
			returnCount++
			value := c.compileExpression(node.Value)
			machine.Return = &value
			if returnCount > 1 {
				c.error(node.Span, "a loop may contain only one return declaration")
			}
		}
	}
	// Traps are emitted after every verifier regardless of source order. The
	// ladder stops at its first failure, so a human is only ever asked about
	// work the machine has already accepted — the scarcest resource is spent
	// last.
	for _, statement := range goal.Statements {
		if node, ok := statement.(*language.Trap); ok {
			gateCount++
			trap := c.compileTrap(node)
			machine.States = append(machine.States, ir.State{Kind: ir.StateTrap, Executor: ir.ExecutorControl, Name: node.Name, Trap: trap, Span: node.Span})
		}
	}

	// A goal needs a gate, but not necessarily a machine-checkable one. Work
	// whose only standard of completion is a person's judgement — a piece of
	// writing, a design call — is gated entirely by traps, and demanding a
	// token verifier there would only dress a subjective goal up as an
	// objective one.
	if gateCount == 0 {
		c.error(machine.Span, "goal must contain at least one gate: a verify Hamster can check or a trap a human answers")
	}
	c.appendLoopTerminalStates(machine)
}

func (c *compiler) compileFlowStatements(machine *ir.Machine, statements []language.Statement) {
	for _, statement := range statements {
		switch node := statement.(type) {
		case *language.Step:
			state := ir.State{Kind: ir.StateStep, Executor: ir.ExecutorAgent, Name: node.Name, Span: node.Span}
			switch value := node.Value.(type) {
			case *language.LoopValue:
				state.Kind = ir.StateNested
				state.Nested = c.compileLoop(value.Loop)
			case *language.Fork:
				state.Kind = ir.StateFork
			case language.Expr:
				expression := c.compileExpression(value)
				state.Expression = &expression
			}
			machine.States = append(machine.States, state)
		case *language.If:
			condition := c.compileExpression(node.Condition)
			branchIndex := len(machine.States)
			machine.States = append(machine.States, ir.State{Kind: ir.StateBranch, Executor: ir.ExecutorControl, Name: "if", Expression: &condition, Span: node.Span})
			bodyStart := len(machine.States)
			c.compileFlowStatements(machine, node.Statements)
			afterBody := len(machine.States)
			machine.States[branchIndex].OnSuccess = stateTarget(bodyStart)
			machine.States[branchIndex].OnFailure = stateTarget(afterBody)
		case *language.Exec:
			if node.Script == "" {
				c.error(node.Span, "exec requires a script name")
			}
			machine.States = append(machine.States, ir.State{Kind: ir.StateExec, Executor: ir.ExecutorRuntime, Name: "exec", Script: node.Script, Span: node.Span})
		case *language.Wait:
			c.validateWaitTarget(node)
			expression := c.compileExpression(node.Target)
			machine.States = append(machine.States, ir.State{Kind: ir.StateWait, Executor: ir.ExecutorRuntime, Name: "wait", Expression: &expression, Span: node.Span})
		case *language.Trap:
			trap := c.compileTrap(node)
			machine.States = append(machine.States, ir.State{Kind: ir.StateTrap, Executor: ir.ExecutorControl, Name: node.Name, Trap: trap, Span: node.Span})
		case *language.Judge:
			c.compileJudge(machine, node)
		}
	}
}

// compileTrap lowers a supervisor request. Hamster cannot compute a trap's
// answer, so the compiler's whole job is to guarantee the emitted state is
// answerable: a known request form, a static prompt, and — for a selection — a
// closed set of options the supervisor can actually choose between.
func (c *compiler) compileTrap(trap *language.Trap) *ir.Trap {
	call, ok := trap.Request.(*language.Call)
	if !ok {
		c.error(trap.Span, "trap requires user.approve(PROMPT) or user.select(PROMPT, options: [...])")
		return &ir.Trap{Kind: ir.TrapApproval}
	}
	path := callPath(call.Callee)
	result := &ir.Trap{}
	switch path {
	case "user.approve":
		result.Kind = ir.TrapApproval
	case "user.select":
		result.Kind = ir.TrapSelection
	default:
		c.error(trap.Span, fmt.Sprintf("trap cannot request %q; supported requests are user.approve and user.select", path))
		return &ir.Trap{Kind: ir.TrapApproval}
	}

	var options *language.CallArgument
	var prompt language.Expr
	for index := range call.Arguments {
		argument := call.Arguments[index]
		switch argument.Name {
		case "":
			if prompt != nil {
				c.error(argument.Span, path+" accepts one prompt")
				continue
			}
			prompt = argument.Value
		case "options":
			if result.Kind != ir.TrapSelection {
				c.error(argument.Span, "options may only be supplied to user.select")
				continue
			}
			options = &call.Arguments[index]
		default:
			c.error(argument.Span, fmt.Sprintf("%s does not accept argument %q", path, argument.Name))
		}
	}

	if prompt == nil {
		c.error(trap.Span, path+" requires a prompt")
		return result
	}
	literal, ok := prompt.(*language.Literal)
	if !ok || literal.Kind != "string" || strings.TrimSpace(literal.Value) == "" {
		c.error(prompt.NodeSpan(), "trap prompt must be a non-empty string literal so it can be reviewed before the run starts")
		return result
	}
	result.Prompt = c.compileExpression(prompt)

	if result.Kind == ir.TrapSelection {
		if options == nil {
			c.error(trap.Span, "user.select requires options: [...]")
			return result
		}
		list, ok := options.Value.(*language.List)
		if !ok || len(list.Values) < 2 {
			c.error(options.Span, "user.select requires a static list of at least two options")
			return result
		}
		for _, value := range list.Values {
			result.Options = append(result.Options, c.compileExpression(value))
		}
	}
	return result
}

// compileJudge desugars a subjective check into process primitives. There is no
// judge instruction in the runtime: a judge is a fork into a constrained
// observer, an exec that clears the working agent's context, a wait, and a
// verdict dispatch. That is what buys the observer a fresh context and an
// allow-list it cannot escape, rather than a prompt asking it to behave.
func (c *compiler) compileJudge(machine *ir.Machine, judge *language.Judge) {
	evidence, options, ok := c.compileJudgeRequest(judge)
	if !ok {
		return
	}
	handle := "__judge_" + judge.Name
	prepareIndex := len(machine.States)
	forkIndex := prepareIndex + 1
	branchIndex := forkIndex + 1
	execIndex := branchIndex + 1
	waitIndex := execIndex + 1
	verdictIndex := waitIndex + 1
	_ = verdictIndex
	escalateIndex := verdictIndex + 1

	observer := c.observerMachine(judge)
	childCondition := ir.Expression{
		Kind: ir.ExpressionMember, Name: "child", Span: judge.Span,
		Receiver: &ir.Expression{Kind: ir.ExpressionIdentifier, Name: handle, Span: judge.Span},
	}
	handleReference := ir.Expression{Kind: ir.ExpressionIdentifier, Name: handle, Span: judge.Span}

	machine.States = append(machine.States,
		ir.State{
			Kind: ir.StateJudge, Executor: ir.ExecutorRuntime, Name: judge.Name, Span: judge.Span,
			Judge: &ir.Judge{Clause: judge.Name, Evidence: evidence, Escalate: stateTarget(escalateIndex),
				EscalateAfter: options.escalateAfter, AuditRate: options.auditRate},
		},
		ir.State{Kind: ir.StateFork, Executor: ir.ExecutorAgent, Name: handle, Span: judge.Span},
		ir.State{Kind: ir.StateBranch, Executor: ir.ExecutorControl, Name: "if", Expression: &childCondition, Span: judge.Span,
			OnSuccess: stateTarget(execIndex), OnFailure: stateTarget(waitIndex)},
		ir.State{Kind: ir.StateExec, Executor: ir.ExecutorRuntime, Name: "exec", Span: judge.Span,
			Machine: observer, JudgeRef: stateTarget(prepareIndex), Next: stateTarget(waitIndex)},
		ir.State{Kind: ir.StateWait, Executor: ir.ExecutorRuntime, Name: "wait", Expression: &handleReference, Span: judge.Span},
		ir.State{Kind: ir.StateVerdict, Executor: ir.ExecutorRuntime, Name: judge.Name, Span: judge.Span,
			Next: stateTarget(escalateIndex + 1), JudgeRef: stateTarget(prepareIndex),
			Judge: &ir.Judge{Clause: judge.Name, Escalate: stateTarget(escalateIndex),
				EscalateAfter: options.escalateAfter, AuditRate: options.auditRate}},
		// An observer that cannot decide does not get to invent an answer. It
		// abstains, and abstention is a transfer of control to the supervisor.
		ir.State{Kind: ir.StateTrap, Executor: ir.ExecutorControl, Name: judge.Name + "_escalated", Span: judge.Span,
			Conditional: true,
			Trap: &ir.Trap{Kind: ir.TrapApproval, Clause: judge.Name, Prompt: ir.Expression{
				Kind: ir.ExpressionLiteral, ValueKind: "string", Span: judge.Span,
				Value: "The observer could not decide \"" + judge.Name + "\" from the ratified taste profile. Approve this work?",
			}},
		},
	)
}

type judgeOptions struct {
	escalateAfter int
	auditRate     int
}

func (c *compiler) compileJudgeRequest(judge *language.Judge) ([]ir.Expression, judgeOptions, bool) {
	// An observer that fails the same clause repeatedly is reporting that the
	// agent cannot perceive the problem, which is a human's call rather than
	// something to retry until the attempt budget runs out.
	options := judgeOptions{escalateAfter: defaultEscalateAfter}
	call, ok := judge.Request.(*language.Call)
	if !ok || callPath(call.Callee) != "taste.check" {
		c.error(judge.Span, "judge requires taste.check(EVIDENCE); an observer applies a ratified clause and has no standard of its own")
		return nil, options, false
	}
	var evidenceArgument *language.CallArgument
	for index := range call.Arguments {
		argument := call.Arguments[index]
		switch argument.Name {
		case "":
			if evidenceArgument != nil {
				c.error(argument.Span, "taste.check accepts one evidence argument")
				return nil, options, false
			}
			evidenceArgument = &call.Arguments[index]
		case "escalate_after":
			value, ok := c.positiveInteger(argument, "escalate_after")
			if !ok {
				return nil, options, false
			}
			options.escalateAfter = value
		case "audit":
			value, ok := c.positiveInteger(argument, "audit")
			if !ok {
				return nil, options, false
			}
			options.auditRate = value
		default:
			c.error(argument.Span, fmt.Sprintf("taste.check does not accept argument %q", argument.Name))
			return nil, options, false
		}
	}
	if evidenceArgument == nil {
		c.error(judge.Span, "taste.check accepts one evidence argument")
		return nil, options, false
	}
	var sources []language.Expr
	switch value := evidenceArgument.Value.(type) {
	case *language.Literal:
		sources = []language.Expr{value}
	case *language.List:
		if len(value.Values) == 0 {
			c.error(value.Span, "taste.check requires at least one evidence command")
			return nil, options, false
		}
		sources = value.Values
	default:
		c.error(evidenceArgument.Span, "taste.check evidence must be a static command or list of commands")
		return nil, options, false
	}
	evidence := make([]ir.Expression, 0, len(sources))
	for _, source := range sources {
		literal, ok := source.(*language.Literal)
		// Evidence is gathered by the parent from statically declared commands.
		// A computed command would let the work under review choose what the
		// observer is allowed to see.
		if !ok || literal.Kind != "string" || strings.TrimSpace(literal.Value) == "" {
			c.error(source.NodeSpan(), "evidence must be a non-empty string literal so it is reviewable before the run starts")
			return nil, options, false
		}
		evidence = append(evidence, c.compileExpression(source))
	}
	return evidence, options, true
}

const defaultEscalateAfter = 3

func (c *compiler) positiveInteger(argument language.CallArgument, name string) (int, bool) {
	literal, ok := argument.Value.(*language.Literal)
	if !ok || literal.Kind != "number" {
		c.error(argument.Span, name+" requires a positive integer literal")
		return 0, false
	}
	value, err := strconv.Atoi(strings.ReplaceAll(literal.Value, "_", ""))
	if err != nil || value <= 0 {
		c.error(argument.Span, name+" requires a positive integer literal")
		return 0, false
	}
	return value, true
}

// observerMachine synthesizes the program the observer subagent runs. Its
// allow-list is the enforcement: the observer may run Hamster commands and
// nothing else, so it can read the profile and the evidence and record a
// verdict, but it structurally cannot edit code or run the test suite.
func (c *compiler) observerMachine(judge *language.Judge) *ir.Machine {
	machine := &ir.Machine{
		ID: fmt.Sprintf("machine_%d", c.nextMachine), Kind: ir.LoopGoal,
		Name: "observer:" + judge.Name, Span: judge.Span,
	}
	c.nextMachine++
	literal := func(value string) ir.Expression {
		return ir.Expression{Kind: ir.ExpressionLiteral, ValueKind: "string", Value: value, Span: judge.Span}
	}
	recorded := ir.Expression{
		Kind: ir.ExpressionCall, Span: judge.Span,
		Callee: &ir.Expression{
			Kind: ir.ExpressionMember, Name: "recorded", Span: judge.Span,
			Receiver: &ir.Expression{Kind: ir.ExpressionIdentifier, Name: "judge", Span: judge.Span},
		},
	}
	machine.States = []ir.State{
		// Time is the only bound that matters here. An observer that never
		// speaks must not hang the parent, and its silence becomes an
		// abstention rather than a guess.
		{Kind: ir.StateLimit, Executor: ir.ExecutorControl, Name: "time",
			Limit: &ir.Limit{Kind: "time", Value: literal(observerDeadline)}, Span: judge.Span},
		{Kind: ir.StatePolicy, Executor: ir.ExecutorControl, Name: "allow",
			Policy: &ir.Policy{Allow: true, Value: ir.Expression{Kind: ir.ExpressionIdentifier, Name: "hamster", Span: judge.Span}}, Span: judge.Span},
		{Kind: ir.StateAgentWork, Executor: ir.ExecutorAgent, Name: "observe", Span: judge.Span},
		{Kind: ir.StateVerify, Executor: ir.ExecutorRuntime, Name: "spoke", Expression: &recorded, Span: judge.Span},
	}
	machine.Return = &ir.Expression{Kind: ir.ExpressionIdentifier, Name: "spoke", Span: judge.Span}
	c.appendLoopTerminalStates(machine)
	return machine
}

const observerDeadline = "15m"

func callPath(expression language.Expr) string {
	switch node := expression.(type) {
	case *language.Identifier:
		return node.Name
	case *language.Member:
		return callPath(node.Receiver) + "." + node.Name
	default:
		return ""
	}
}

func (c *compiler) validateWaitTarget(wait *language.Wait) {
	var validate func(language.Expr) bool
	validate = func(expression language.Expr) bool {
		switch node := expression.(type) {
		case *language.Identifier:
			return true
		case *language.List:
			if len(node.Values) == 0 {
				return false
			}
			for _, value := range node.Values {
				if _, ok := value.(*language.Identifier); !ok {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	if !validate(wait.Target) {
		c.error(wait.Span, "wait requires a fork handle or non-empty list of fork handles")
	}
}

func stateTarget(index int) string { return fmt.Sprintf("@state:%d", index) }

func (c *compiler) validateLimit(limit *language.Limit) {
	literal, ok := limit.Value.(*language.Literal)
	if !ok {
		c.error(limit.Span, fmt.Sprintf("limit %s requires a static literal", limit.Kind))
		return
	}
	switch limit.Kind {
	case "attempts":
		value, err := strconv.Atoi(strings.ReplaceAll(literal.Value, "_", ""))
		if err != nil || value <= 0 {
			c.error(limit.Span, "limit attempts requires a positive integer")
		}
	case "time":
		value, err := time.ParseDuration(literal.Value)
		if err != nil || value <= 0 {
			c.error(limit.Span, "limit time requires a positive duration such as 30m")
		}
	case "traps":
		value, err := strconv.Atoi(strings.ReplaceAll(literal.Value, "_", ""))
		if err != nil || value <= 0 {
			c.error(limit.Span, "limit traps requires a positive integer")
		}
	case "judgements":
		value, err := strconv.Atoi(strings.ReplaceAll(literal.Value, "_", ""))
		if err != nil || value <= 0 {
			c.error(limit.Span, "limit judgements requires a positive integer")
		}
	default:
		c.error(limit.Span, fmt.Sprintf("limit kind %q is not enforceable by this runtime; supported kinds are attempts, time, traps, and judgements", limit.Kind))
	}
}

func (c *compiler) appendLoopTerminalStates(machine *ir.Machine) {
	boundaryIndex := len(machine.States)
	machine.States = append(machine.States,
		ir.State{Kind: ir.StateBoundary, Executor: ir.ExecutorControl, Name: "iteration_boundary", Span: machine.Span},
		ir.State{Kind: ir.StateComplete, Executor: ir.ExecutorControl, Name: "complete", Span: machine.Span},
	)
	c.assignStateIDs(machine)
	machine.InitialState = machine.States[0].ID
	boundary := &machine.States[boundaryIndex]
	boundary.OnSuccess = machine.States[boundaryIndex+1].ID
	boundary.OnFailure = machine.InitialState
	for index := range machine.States {
		// A rejected trap re-derives the work the same way a failed verifier
		// does, so the supervisor's verdict costs a re-run and never a rewrite.
		switch machine.States[index].Kind {
		case ir.StateVerify, ir.StateTrap, ir.StateVerdict:
			machine.States[index].OnFailure = machine.InitialState
		}
	}
}

func (c *compiler) compileForeachStates(machine *ir.Machine) {
	machine.States = []ir.State{
		{Kind: ir.StateSequence, Executor: ir.ExecutorRuntime, Name: "evaluate_sequence", Span: machine.Span},
		{Kind: ir.StateElement, Executor: ir.ExecutorControl, Name: "run_element", Nested: machine.Foreach.Body, Span: machine.Span},
		{Kind: ir.StateBoundary, Executor: ir.ExecutorControl, Name: "collection_boundary", Span: machine.Span},
		{Kind: ir.StateComplete, Executor: ir.ExecutorControl, Name: "complete", Span: machine.Span},
	}
	c.assignStateIDs(machine)
	machine.InitialState = machine.States[0].ID
	machine.States[2].OnSuccess = machine.States[3].ID
	machine.States[2].OnFailure = machine.States[1].ID
}

func (c *compiler) assignStateIDs(machine *ir.Machine) {
	for index := range machine.States {
		machine.States[index].ID = fmt.Sprintf("state_%d", index)
	}
	for index := range machine.States {
		if machine.States[index].Next == "" && index+1 < len(machine.States) && machine.States[index].Kind != ir.StateBoundary {
			machine.States[index].Next = machine.States[index+1].ID
		}
	}
	for index := range machine.States {
		state := &machine.States[index]
		state.Next = resolveStateTarget(machine, state.Next)
		state.OnSuccess = resolveStateTarget(machine, state.OnSuccess)
		state.OnFailure = resolveStateTarget(machine, state.OnFailure)
		state.JudgeRef = resolveStateTarget(machine, state.JudgeRef)
		if state.Judge != nil {
			state.Judge.Escalate = resolveStateTarget(machine, state.Judge.Escalate)
		}
	}
}

func resolveStateTarget(machine *ir.Machine, target string) string {
	if !strings.HasPrefix(target, "@state:") {
		return target
	}
	index, err := strconv.Atoi(strings.TrimPrefix(target, "@state:"))
	if err != nil || index < 0 || index >= len(machine.States) {
		return ""
	}
	return machine.States[index].ID
}

func (c *compiler) compileExpression(expression language.Expr) ir.Expression {
	result := ir.Expression{Span: expression.NodeSpan()}
	switch node := expression.(type) {
	case *language.Identifier:
		result.Kind, result.Name = ir.ExpressionIdentifier, node.Name
	case *language.Literal:
		result.Kind, result.ValueKind, result.Value = ir.ExpressionLiteral, node.Kind, node.Value
	case *language.Member:
		receiver := c.compileExpression(node.Receiver)
		result.Kind, result.Receiver, result.Name = ir.ExpressionMember, &receiver, node.Name
	case *language.Call:
		callee := c.compileExpression(node.Callee)
		result.Kind, result.Callee = ir.ExpressionCall, &callee
		for _, argument := range node.Arguments {
			result.Arguments = append(result.Arguments, ir.Argument{Name: argument.Name, Value: c.compileExpression(argument.Value)})
		}
	case *language.Binary:
		left, right := c.compileExpression(node.Left), c.compileExpression(node.Right)
		result.Kind, result.Left, result.Right, result.Operator = ir.ExpressionBinary, &left, &right, string(node.Operator)
	case *language.Unary:
		operand := c.compileExpression(node.Value)
		result.Kind, result.Operand, result.Operator = ir.ExpressionUnary, &operand, string(node.Operator)
	case *language.List:
		result.Kind = ir.ExpressionList
		for _, element := range node.Values {
			result.Elements = append(result.Elements, c.compileExpression(element))
		}
	default:
		panic(fmt.Sprintf("unsupported expression type %T", expression))
	}
	return result
}

func (c *compiler) error(span language.Span, message string) {
	c.diagnostics = append(c.diagnostics, language.Diagnostic{Span: span, Message: message})
}
