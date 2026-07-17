package compiler

import (
	"fmt"

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
	seenNames := map[string]language.Span{}
	for _, loop := range program.Loops {
		if name := loop.LoopName(); name != "" {
			if previous, exists := seenNames[name]; exists {
				c.error(loop.NodeSpan(), fmt.Sprintf("loop %q is already defined at %s", name, previous))
			} else {
				seenNames[name] = loop.NodeSpan()
			}
		}
		result.Machines = append(result.Machines, *c.compileLoop(loop))
	}
	if len(result.Machines) == 0 {
		c.error(language.Span{}, "program must contain at least one loop")
	}
	return result, c.diagnostics
}

func (c *compiler) compileLoop(loop language.Loop) *ir.Machine {
	machine := &ir.Machine{
		ID:   fmt.Sprintf("machine_%d", c.nextMachine),
		Name: loop.LoopName(),
		Span: loop.NodeSpan(),
	}
	c.nextMachine++

	switch node := loop.(type) {
	case *language.Goal:
		machine.Kind = ir.LoopGoal
		c.compileStatements(machine, node.Statements, true)
	case *language.Flow:
		machine.Kind = ir.LoopFlow
		c.compileStatements(machine, node.Statements, false)
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

func (c *compiler) compileStatements(machine *ir.Machine, statements []language.Statement, goal bool) {
	if goal {
		// Goal constraints are active before Codex is given an agent-work state.
		// There are no ordered agent steps in a goal, so prospective limits begin
		// at goal entry rather than after the first arbitrary tool call.
		for _, statement := range statements {
			switch node := statement.(type) {
			case *language.Limit:
				value := c.compileExpression(node.Value)
				machine.States = append(machine.States, ir.State{Kind: ir.StateLimit, Executor: ir.ExecutorControl, Name: node.Kind, Limit: &ir.Limit{Kind: node.Kind, Value: value}, Span: node.Span})
			case *language.Policy:
				value := c.compileExpression(node.Value)
				machine.States = append(machine.States, ir.State{Kind: ir.StatePolicy, Executor: ir.ExecutorControl, Policy: &ir.Policy{Allow: node.Allow, Value: value}, Span: node.Span})
			}
		}
		machine.States = append(machine.States, ir.State{
			Kind:     ir.StateAgentWork,
			Executor: ir.ExecutorAgent,
			Name:     "work",
			Span:     machine.Span,
		})
	}

	verificationCount := 0
	returnCount := 0
	for _, statement := range statements {
		switch node := statement.(type) {
		case *language.Step:
			state := ir.State{Kind: ir.StateStep, Executor: ir.ExecutorAgent, Name: node.Name, Span: node.Span}
			switch value := node.Value.(type) {
			case *language.LoopValue:
				state.Kind = ir.StateNested
				state.Nested = c.compileLoop(value.Loop)
			case language.Expr:
				expression := c.compileExpression(value)
				state.Expression = &expression
			default:
				panic(fmt.Sprintf("unsupported step value %T", node.Value))
			}
			machine.States = append(machine.States, state)
		case *language.Verify:
			verificationCount++
			expression := c.compileExpression(node.Check)
			machine.States = append(machine.States, ir.State{Kind: ir.StateVerify, Executor: ir.ExecutorRuntime, Name: node.Name, Expression: &expression, Span: node.Span})
		case *language.Limit:
			if goal {
				continue
			}
			value := c.compileExpression(node.Value)
			machine.States = append(machine.States, ir.State{Kind: ir.StateLimit, Executor: ir.ExecutorControl, Name: node.Kind, Limit: &ir.Limit{Kind: node.Kind, Value: value}, Span: node.Span})
		case *language.Policy:
			if goal {
				continue
			}
			value := c.compileExpression(node.Value)
			machine.States = append(machine.States, ir.State{Kind: ir.StatePolicy, Executor: ir.ExecutorControl, Policy: &ir.Policy{Allow: node.Allow, Value: value}, Span: node.Span})
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
		c.error(machine.Span, fmt.Sprintf("%s loop must contain at least one verification", machine.Kind))
	}
	c.appendLoopTerminalStates(machine)
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
