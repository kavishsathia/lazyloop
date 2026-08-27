package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"hamster/internal/ir"
)

type Environment map[string]any

type Evaluator struct {
	WorkingDirectory string
	Verifier         Verifier
	RunID            string
	// Output, when set, receives what the most recent bash.check printed, so
	// a failed verifier can be reported with its own words.
	Output *string
	// Observed reports whether this run has recorded a verdict. It is what
	// makes "the observer must speak" a machine-checked postcondition rather
	// than an instruction the observer could ignore.
	Observed func(ctx context.Context, observerRunID string) (bool, error)
}

func (e Evaluator) Evaluate(ctx context.Context, expression ir.Expression, environment Environment) (any, error) {
	switch expression.Kind {
	case ir.ExpressionIdentifier:
		value, ok := environment[expression.Name]
		if !ok {
			return nil, fmt.Errorf("unknown value %q", expression.Name)
		}
		return value, nil
	case ir.ExpressionLiteral:
		switch expression.ValueKind {
		case "string":
			return expression.Value, nil
		case "boolean":
			return expression.Value == "true", nil
		case "number":
			if number, err := strconv.ParseFloat(strings.ReplaceAll(expression.Value, "_", ""), 64); err == nil {
				return number, nil
			}
			return expression.Value, nil
		}
	case ir.ExpressionMember:
		receiver, err := e.Evaluate(ctx, *expression.Receiver, environment)
		if err != nil {
			return nil, err
		}
		return member(receiver, expression.Name)
	case ir.ExpressionCall:
		return e.call(ctx, expression, environment)
	case ir.ExpressionBinary:
		left, err := e.Evaluate(ctx, *expression.Left, environment)
		if err != nil {
			return nil, err
		}
		right, err := e.Evaluate(ctx, *expression.Right, environment)
		if err != nil {
			return nil, err
		}
		return binary(expression.Operator, left, right)
	case ir.ExpressionUnary:
		value, err := e.Evaluate(ctx, *expression.Operand, environment)
		if err != nil {
			return nil, err
		}
		if expression.Operator == "!" {
			return !truthy(value), nil
		}
	case ir.ExpressionList:
		values := make([]any, 0, len(expression.Elements))
		for _, element := range expression.Elements {
			value, err := e.Evaluate(ctx, element, environment)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	}
	return nil, fmt.Errorf("cannot evaluate expression kind %q", expression.Kind)
}

func (e Evaluator) call(ctx context.Context, expression ir.Expression, environment Environment) (any, error) {
	path := expressionPath(*expression.Callee)
	arguments := make([]any, 0, len(expression.Arguments))
	for _, argument := range expression.Arguments {
		value, err := e.Evaluate(ctx, argument.Value, environment)
		if err != nil {
			return nil, err
		}
		arguments = append(arguments, value)
	}
	switch path {
	case "judge.recorded":
		if len(arguments) != 0 {
			return nil, errors.New("judge.recorded takes no arguments")
		}
		if e.Observed == nil || e.RunID == "" {
			return nil, errors.New("judge.recorded is only meaningful inside an observer run")
		}
		return e.Observed(ctx, e.RunID)
	case "bash.check":
		if len(arguments) != 1 {
			return nil, errors.New("bash.check expects one command")
		}
		command, ok := arguments[0].(string)
		if !ok {
			return nil, errors.New("bash.check command must be a string")
		}
		verifier := e.Verifier
		if verifier == nil {
			verifier = CommandVerifier{}
		}
		passed, output, err := verifier.Check(ctx, e.WorkingDirectory, command)
		if e.Output != nil {
			*e.Output = output
		}
		return passed, err
	default:
		return nil, fmt.Errorf("runtime cannot execute %q", path)
	}
}

func member(receiver any, name string) (any, error) {
	switch value := receiver.(type) {
	case map[string]any:
		result, ok := value[name]
		if !ok {
			return nil, fmt.Errorf("value has no member %q", name)
		}
		return result, nil
	case []any:
		if name == "count" {
			return float64(len(value)), nil
		}
	}
	return nil, fmt.Errorf("%T has no member %q", receiver, name)
}

func binary(operator string, left, right any) (any, error) {
	switch operator {
	case "==":
		return reflect.DeepEqual(left, right), nil
	case "!=":
		return !reflect.DeepEqual(left, right), nil
	case "&&":
		return truthy(left) && truthy(right), nil
	case "||":
		return truthy(left) || truthy(right), nil
	case "<", "<=", ">", ">=":
		l, lok := numeric(left)
		r, rok := numeric(right)
		if !lok || !rok {
			return nil, fmt.Errorf("operator %s requires numeric values", operator)
		}
		switch operator {
		case "<":
			return l < r, nil
		case "<=":
			return l <= r, nil
		case ">":
			return l > r, nil
		default:
			return l >= r, nil
		}
	}
	return nil, fmt.Errorf("unsupported operator %q", operator)
}

func numeric(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok
}

func truthy(value any) bool {
	switch value := value.(type) {
	case bool:
		return value
	case nil:
		return false
	case string:
		return value != ""
	default:
		return true
	}
}

func expressionPath(expression ir.Expression) string {
	if expression.Kind == ir.ExpressionIdentifier {
		return expression.Name
	}
	if expression.Kind == ir.ExpressionMember && expression.Receiver != nil {
		return expressionPath(*expression.Receiver) + "." + expression.Name
	}
	return ""
}
