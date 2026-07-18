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
	verificationCount := 0
	returnCount := 0
	for _, statement := range goal.Statements {
		switch node := statement.(type) {
		case *language.Verify:
			verificationCount++
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

	if verificationCount == 0 {
		c.error(machine.Span, "goal must contain at least one verification")
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
		}
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
	default:
		c.error(limit.Span, fmt.Sprintf("limit kind %q is not enforceable by this runtime; supported kinds are attempts and time", limit.Kind))
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
		if machine.States[index].Kind == ir.StateVerify {
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
		machine.States[index].OnSuccess = resolveStateTarget(machine, machine.States[index].OnSuccess)
		machine.States[index].OnFailure = resolveStateTarget(machine, machine.States[index].OnFailure)
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
