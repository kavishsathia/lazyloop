package compiler

import (
	"fmt"
	"sort"
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
		result.Machines = append(result.Machines, *c.compileLoop(loop, nil))
	}
	if len(result.Machines) == 0 {
		c.error(language.Span{}, "program must contain at least one loop")
	} else if len(result.Machines) > 1 {
		c.error(program.Loops[1].NodeSpan(), "program must contain exactly one anonymous root loop")
	}
	return result, c.diagnostics
}

func (c *compiler) compileLoop(loop language.Loop, scope map[string]bool) *ir.Machine {
	machine := &ir.Machine{
		ID: fmt.Sprintf("machine_%d", c.nextMachine), Span: loop.NodeSpan(),
	}
	c.nextMachine++

	switch node := loop.(type) {
	case *language.Goal:
		machine.Kind = ir.LoopGoal
		c.compileGoal(machine, node, scope)
	case *language.Foreach:
		machine.Kind = ir.LoopForeach
		machine.Foreach = &ir.Foreach{
			Binding:  node.Binding,
			Sequence: c.compileExpression(node.Sequence),
			Body:     c.compileLoop(node.Body, foreachScope(scope, node.Binding)),
		}
		c.compileForeachStates(machine)
	default:
		panic(fmt.Sprintf("unsupported loop type %T", loop))
	}
	return machine
}

func (c *compiler) compileGoal(machine *ir.Machine, goal *language.Goal, inherited map[string]bool) {
	scope := c.bindings(goal, inherited)
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
		c.compileFlowStatements(machine, goal.Flow.Statements, scope)
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
		switch node := statement.(type) {
		case *language.Trap:
			gateCount++
			machine.States = append(machine.States, ir.State{
				Kind: ir.StateTrap, Executor: ir.ExecutorControl,
				Name: fmt.Sprintf("__trap_%d", len(machine.States)),
				Trap: c.compileTrap(node), Span: node.Span,
			})
		case *language.Judge:
			gateCount++
			c.compileJudge(machine, node)
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
	c.validateReferences(machine, scope)
}

func (c *compiler) compileFlowStatements(machine *ir.Machine, statements []language.Statement, machineScope map[string]bool) {
	for _, statement := range statements {
		switch node := statement.(type) {
		case *language.Step:
			state := ir.State{Kind: ir.StateStep, Executor: ir.ExecutorAgent, Name: node.Name, Span: node.Span}
			switch value := node.Value.(type) {
			case *language.LoopValue:
				state.Kind = ir.StateNested
				state.Nested = c.compileLoop(value.Loop, machineScope)
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
			c.compileFlowStatements(machine, node.Statements, machineScope)
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
			machine.States = append(machine.States, ir.State{
				Kind: ir.StateTrap, Executor: ir.ExecutorControl,
				Name: fmt.Sprintf("__trap_%d", len(machine.States)),
				Trap: c.compileTrap(node), Span: node.Span,
			})
		case *language.Judge:
			c.compileJudge(machine, node)
		}
	}
}

// compileTrap lowers a supervisor request. Hamster cannot compute a trap's
// answer, so the compiler's whole job is to guarantee the emitted state is
// answerable: a written-out question and, for a selection, a closed set of
// options the supervisor can actually choose between.
func (c *compiler) compileTrap(trap *language.Trap) *ir.Trap {
	result := &ir.Trap{Kind: ir.TrapApproval, Prompt: trap.Prompt}
	if len(trap.Options) == 0 {
		return result
	}
	if len(trap.Options) < 2 {
		c.error(trap.Span, "a trap offering choices needs at least two options")
		return result
	}
	result.Kind = ir.TrapSelection
	for _, option := range trap.Options {
		literal, ok := option.(*language.Literal)
		if !ok || literal.Kind != "string" || strings.TrimSpace(literal.Value) == "" {
			c.error(option.NodeSpan(), "trap options must be written-out strings")
			return result
		}
		result.Options = append(result.Options, literal.Value)
	}
	return result
}

// compileJudge desugars a subjective check into process primitives. There is no
// judge instruction in the runtime: a judge is a fork into an observer, an exec
// that clears the working agent's context, a wait, and a verdict dispatch. That
// desugaring is what buys a judgement a reviewer who did not write the work.
func (c *compiler) compileJudge(machine *ir.Machine, judge *language.Judge) {
	handle := fmt.Sprintf("__judge_%d", len(machine.States))
	prepareIndex := len(machine.States)
	forkIndex := prepareIndex + 1
	branchIndex := forkIndex + 1
	execIndex := branchIndex + 1
	waitIndex := execIndex + 1
	verdictIndex := waitIndex + 1
	escalateIndex := verdictIndex + 1

	observer := c.observerMachine(judge)
	childCondition := ir.Expression{
		Kind: ir.ExpressionMember, Name: "child", Span: judge.Span,
		Receiver: &ir.Expression{Kind: ir.ExpressionIdentifier, Name: handle, Span: judge.Span},
	}
	handleReference := ir.Expression{Kind: ir.ExpressionIdentifier, Name: handle, Span: judge.Span}
	request := &ir.Judge{Prompt: judge.Prompt, Escalate: stateTarget(escalateIndex)}

	machine.States = append(machine.States,
		ir.State{Kind: ir.StateJudge, Executor: ir.ExecutorRuntime, Name: handle, Span: judge.Span, Judge: request},
		ir.State{Kind: ir.StateFork, Executor: ir.ExecutorAgent, Name: handle, Span: judge.Span},
		ir.State{Kind: ir.StateBranch, Executor: ir.ExecutorControl, Name: "if", Expression: &childCondition, Span: judge.Span,
			OnSuccess: stateTarget(execIndex), OnFailure: stateTarget(waitIndex)},
		ir.State{Kind: ir.StateExec, Executor: ir.ExecutorRuntime, Name: "exec", Span: judge.Span,
			Machine: observer, JudgeRef: stateTarget(prepareIndex), Next: stateTarget(waitIndex)},
		ir.State{Kind: ir.StateWait, Executor: ir.ExecutorRuntime, Name: "wait", Expression: &handleReference, Span: judge.Span},
		ir.State{Kind: ir.StateVerdict, Executor: ir.ExecutorRuntime, Name: handle, Span: judge.Span,
			Next: stateTarget(escalateIndex + 1), JudgeRef: stateTarget(prepareIndex), Judge: request},
		// An observer that cannot decide does not get to invent an answer. It
		// abstains, and abstention is a transfer of control to the supervisor.
		ir.State{Kind: ir.StateTrap, Executor: ir.ExecutorControl, Name: handle + "_escalated", Span: judge.Span,
			Conditional: true,
			Trap: &ir.Trap{Kind: ir.TrapApproval, Clause: judge.Prompt,
				Prompt: "The observer could not decide whether " + judge.Prompt + ". Approve this work?"},
		},
	)
}

// observerMachine synthesizes the program the observer subagent runs. It may
// investigate the workspace as a reviewer would, but it cannot patch the work
// it is judging, and it cannot finish without recording a cited verdict.
func (c *compiler) observerMachine(judge *language.Judge) *ir.Machine {
	machine := &ir.Machine{
		ID: fmt.Sprintf("machine_%d", c.nextMachine), Kind: ir.LoopGoal,
		Name: "observer", Span: judge.Span,
	}
	c.nextMachine++
	identifier := func(name string) ir.Expression {
		return ir.Expression{Kind: ir.ExpressionIdentifier, Name: name, Span: judge.Span}
	}
	recorded := ir.Expression{
		Kind: ir.ExpressionCall, Span: judge.Span,
		Callee: &ir.Expression{Kind: ir.ExpressionMember, Name: "recorded", Span: judge.Span,
			Receiver: &ir.Expression{Kind: ir.ExpressionIdentifier, Name: "judge", Span: judge.Span}},
	}
	machine.States = []ir.State{
		// Time is the only bound that matters. An observer that never speaks
		// must not hold its parent, and its silence becomes an abstention.
		{Kind: ir.StateLimit, Executor: ir.ExecutorControl, Name: "time",
			Limit: &ir.Limit{Kind: "time", Value: ir.Expression{Kind: ir.ExpressionLiteral, ValueKind: "string", Value: observerDeadline, Span: judge.Span}}, Span: judge.Span},
		{Kind: ir.StatePolicy, Executor: ir.ExecutorControl, Name: "allow",
			Policy: &ir.Policy{Allow: true, Value: identifier("bash")}, Span: judge.Span},
		{Kind: ir.StatePolicy, Executor: ir.ExecutorControl, Name: "allow",
			Policy: &ir.Policy{Allow: true, Value: identifier("hamster")}, Span: judge.Span},
		{Kind: ir.StatePolicy, Executor: ir.ExecutorControl, Name: "disallow",
			Policy: &ir.Policy{Allow: false, Value: identifier("apply_patch")}, Span: judge.Span},
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

// bindings collects every name this loop makes available. Steps, verifiers, a
// foreach binding, and the handles the compiler generates for its own control
// flow are the only things an expression may refer to.
func (c *compiler) bindings(goal *language.Goal, inherited map[string]bool) map[string]bool {
	scope := map[string]bool{}
	for name := range inherited {
		scope[name] = true
	}
	for _, statement := range goal.Statements {
		if node, ok := statement.(*language.Verify); ok {
			scope[node.Name] = true
		}
	}
	if goal.Flow != nil {
		collectStepNames(goal.Flow.Statements, scope)
	}
	return scope
}

func collectStepNames(statements []language.Statement, scope map[string]bool) {
	for _, statement := range statements {
		switch node := statement.(type) {
		case *language.Step:
			scope[node.Name] = true
		case *language.If:
			collectStepNames(node.Statements, scope)
		}
	}
}

// validateReferences reports a name no binding in scope could satisfy. Without
// it a stale reference compiles cleanly and fails at the very end of a run, as
// an internal evaluator error rather than the typo it is.
func (c *compiler) validateReferences(machine *ir.Machine, scope map[string]bool) {
	// Only the handles the compiler generated for its own control flow join the
	// scope. Internal state names are not bindings and must not appear in a
	// diagnostic that tells you what you may refer to.
	for index := range machine.States {
		if name := machine.States[index].Name; strings.HasPrefix(name, "__") {
			scope[name] = true
		}
	}
	for index := range machine.States {
		state := &machine.States[index]
		if state.Kind == ir.StatePolicy || state.Kind == ir.StateLimit || state.Expression == nil {
			continue
		}
		c.checkExpression(*state.Expression, scope)
	}
	if machine.Return != nil {
		c.checkExpression(*machine.Return, scope)
	}
	if machine.Foreach != nil {
		c.checkExpression(machine.Foreach.Sequence, scope)
	}
}

func (c *compiler) checkExpression(expression ir.Expression, scope map[string]bool) {
	switch expression.Kind {
	case ir.ExpressionIdentifier:
		if !scope[expression.Name] {
			c.error(expression.Span, fmt.Sprintf("unknown name %q; %s", expression.Name, describeScope(scope)))
		}
	case ir.ExpressionMember:
		if expression.Receiver != nil {
			c.checkExpression(*expression.Receiver, scope)
		}
	case ir.ExpressionCall:
		// The callee of a call is a builtin path such as bash.check, not a
		// binding, so only the arguments can refer to one.
		for _, argument := range expression.Arguments {
			c.checkExpression(argument.Value, scope)
		}
	case ir.ExpressionBinary:
		if expression.Left != nil {
			c.checkExpression(*expression.Left, scope)
		}
		if expression.Right != nil {
			c.checkExpression(*expression.Right, scope)
		}
	case ir.ExpressionUnary:
		if expression.Operand != nil {
			c.checkExpression(*expression.Operand, scope)
		}
	case ir.ExpressionList:
		for _, element := range expression.Elements {
			c.checkExpression(element, scope)
		}
	}
}

func describeScope(scope map[string]bool) string {
	names := make([]string, 0, len(scope))
	for name := range scope {
		if !strings.HasPrefix(name, "__") && name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "this loop binds no names"
	}
	sort.Strings(names)
	return "this loop binds " + strings.Join(names, ", ")
}

func foreachScope(inherited map[string]bool, binding string) map[string]bool {
	scope := map[string]bool{binding: true}
	for name := range inherited {
		scope[name] = true
	}
	return scope
}
