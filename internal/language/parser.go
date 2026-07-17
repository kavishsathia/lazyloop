package language

import (
	"fmt"
	"strconv"
)

type parser struct {
	tokens      []Token
	current     int
	diagnostics Diagnostics
}

func Parse(source string) (*Program, Diagnostics) {
	tokens, diagnostics := Lex(source)
	p := &parser{tokens: tokens, diagnostics: diagnostics}
	program := &Program{}
	for !p.at(TokenEOF) {
		loop := p.parseLoop(true)
		if loop != nil {
			program.Loops = append(program.Loops, loop)
		} else {
			p.synchronizeTopLevel()
		}
	}
	return program, p.diagnostics
}

func (p *parser) parseLoop(allowName bool) Loop {
	start := p.peek().Span.Start
	kind := p.peek().Kind
	if kind != TokenGoal && kind != TokenFlow && kind != TokenForeach {
		p.errorAt(p.peek(), "expected goal, flow, or foreach")
		return nil
	}
	p.advance()

	if kind == TokenForeach {
		name := ""
		if allowName && p.at(TokenIdentifier) && p.peekNext().Kind == TokenIdentifier {
			name = p.advance().Lexeme
		}
		binding := p.consume(TokenIdentifier, "expected foreach binding")
		p.consume(TokenIn, "expected 'in' after foreach binding")
		sequence := p.parseExpression()
		p.consume(TokenLBrace, "expected '{' before foreach body")
		body := p.parseLoop(false)
		end := p.consume(TokenRBrace, "expected '}' after foreach body")
		if body == nil {
			return nil
		}
		return &Foreach{Span: Span{Start: start, End: end.Span.End}, Name: name, Binding: binding.Lexeme, Sequence: sequence, Body: body}
	}

	name := ""
	if allowName && p.at(TokenIdentifier) {
		name = p.advance().Lexeme
	}

	p.consume(TokenLBrace, "expected '{' before loop body")
	var statements []Statement
	for !p.at(TokenRBrace) && !p.at(TokenEOF) {
		statement := p.parseStatement(kind)
		if statement != nil {
			statements = append(statements, statement)
		} else {
			p.synchronizeStatement()
		}
	}
	end := p.consume(TokenRBrace, "expected '}' after loop body")
	span := Span{Start: start, End: end.Span.End}
	if kind == TokenGoal {
		return &Goal{Span: span, Name: name, Statements: statements}
	}
	return &Flow{Span: span, Name: name, Statements: statements}
}

func (p *parser) parseStatement(loopKind TokenKind) Statement {
	start := p.peek().Span.Start
	switch p.peek().Kind {
	case TokenStep:
		p.advance()
		if loopKind != TokenFlow {
			p.errorAt(p.previous(), "step is only valid inside flow")
		}
		name := p.consume(TokenIdentifier, "expected step name")
		p.consume(TokenAssign, "expected '=' after step name")
		var value Value
		if p.at(TokenGoal) || p.at(TokenFlow) || p.at(TokenForeach) {
			value = &LoopValue{Loop: p.parseLoop(false)}
		} else {
			value = p.parseExpression()
		}
		end := p.consume(TokenSemicolon, "expected ';' after step")
		return &Step{Span: Span{Start: start, End: end.Span.End}, Name: name.Lexeme, Value: value}
	case TokenVerify:
		p.advance()
		name := p.consume(TokenIdentifier, "expected verification name")
		p.consume(TokenAssign, "expected '=' after verification name")
		check := p.parseExpression()
		end := p.consume(TokenSemicolon, "expected ';' after verification")
		return &Verify{Span: Span{Start: start, End: end.Span.End}, Name: name.Lexeme, Check: check}
	case TokenLimit:
		p.advance()
		kind := p.consume(TokenIdentifier, "expected limit kind")
		value := p.parseExpression()
		end := p.consume(TokenSemicolon, "expected ';' after limit")
		return &Limit{Span: Span{Start: start, End: end.Span.End}, Kind: kind.Lexeme, Value: value}
	case TokenReturn:
		p.advance()
		value := p.parseExpression()
		end := p.consume(TokenSemicolon, "expected ';' after return")
		return &Return{Span: Span{Start: start, End: end.Span.End}, Value: value}
	case TokenAllow, TokenDisallow:
		allow := p.advance().Kind == TokenAllow
		value := p.parseExpression()
		end := p.consume(TokenSemicolon, "expected ';' after policy")
		return &Policy{Span: Span{Start: start, End: end.Span.End}, Allow: allow, Value: value}
	default:
		p.errorAt(p.peek(), "expected step, verify, limit, return, allow, or disallow")
		return nil
	}
}

func (p *parser) parseExpression() Expr { return p.parseBinary(1) }

var precedence = map[TokenKind]int{
	TokenOr: 1, TokenAnd: 2,
	TokenEqual: 3, TokenNotEqual: 3,
	TokenLess: 4, TokenLessEqual: 4, TokenGreater: 4, TokenGreaterEq: 4,
}

func (p *parser) parseBinary(minPrecedence int) Expr {
	left := p.parseUnary()
	for {
		operator := p.peek()
		operatorPrecedence, ok := precedence[operator.Kind]
		if !ok || operatorPrecedence < minPrecedence {
			break
		}
		p.advance()
		right := p.parseBinary(operatorPrecedence + 1)
		left = &Binary{Span: Span{Start: left.NodeSpan().Start, End: right.NodeSpan().End}, Left: left, Operator: operator.Kind, Right: right}
	}
	return left
}

func (p *parser) parseUnary() Expr {
	if p.at(TokenBang) {
		operator := p.advance()
		value := p.parseUnary()
		return &Unary{Span: Span{Start: operator.Span.Start, End: value.NodeSpan().End}, Operator: operator.Kind, Value: value}
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() Expr {
	expression := p.parsePrimary()
	for {
		switch {
		case p.match(TokenDot):
			name := p.consume(TokenIdentifier, "expected member name after '.'")
			expression = &Member{Span: Span{Start: expression.NodeSpan().Start, End: name.Span.End}, Receiver: expression, Name: name.Lexeme}
		case p.match(TokenLParen):
			var arguments []CallArgument
			for !p.at(TokenRParen) && !p.at(TokenEOF) {
				start := p.peek().Span.Start
				name := ""
				if p.at(TokenIdentifier) && p.peekNext().Kind == TokenColon {
					name = p.advance().Lexeme
					p.advance()
				}
				value := p.parseExpression()
				arguments = append(arguments, CallArgument{Span: Span{Start: start, End: value.NodeSpan().End}, Name: name, Value: value})
				if !p.match(TokenComma) {
					break
				}
			}
			end := p.consume(TokenRParen, "expected ')' after arguments")
			expression = &Call{Span: Span{Start: expression.NodeSpan().Start, End: end.Span.End}, Callee: expression, Arguments: arguments}
		default:
			return expression
		}
	}
}

func (p *parser) parsePrimary() Expr {
	token := p.advance()
	switch token.Kind {
	case TokenIdentifier:
		return &Identifier{Span: token.Span, Name: token.Lexeme}
	case TokenString:
		value, err := strconv.Unquote(token.Lexeme)
		if err != nil {
			p.errorAt(token, "invalid string literal")
			value = token.Lexeme
		}
		return &Literal{Span: token.Span, Kind: "string", Value: value}
	case TokenNumber:
		return &Literal{Span: token.Span, Kind: "number", Value: token.Lexeme}
	case TokenTrue, TokenFalse:
		return &Literal{Span: token.Span, Kind: "boolean", Value: token.Lexeme}
	case TokenLParen:
		expression := p.parseExpression()
		p.consume(TokenRParen, "expected ')' after expression")
		return expression
	case TokenLBracket:
		var values []Expr
		for !p.at(TokenRBracket) && !p.at(TokenEOF) {
			values = append(values, p.parseExpression())
			if !p.match(TokenComma) {
				break
			}
		}
		end := p.consume(TokenRBracket, "expected ']' after list")
		return &List{Span: Span{Start: token.Span.Start, End: end.Span.End}, Values: values}
	default:
		p.errorAt(token, fmt.Sprintf("expected expression, found %q", token.Lexeme))
		return &Identifier{Span: token.Span, Name: "<error>"}
	}
}

func (p *parser) consume(kind TokenKind, message string) Token {
	if p.at(kind) {
		return p.advance()
	}
	p.errorAt(p.peek(), message)
	return Token{Kind: kind, Span: p.peek().Span}
}

func (p *parser) match(kind TokenKind) bool {
	if !p.at(kind) {
		return false
	}
	p.advance()
	return true
}

func (p *parser) at(kind TokenKind) bool { return p.peek().Kind == kind }

func (p *parser) peek() Token { return p.tokens[p.current] }

func (p *parser) peekNext() Token {
	if p.current+1 >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[p.current+1]
}

func (p *parser) previous() Token { return p.tokens[p.current-1] }

func (p *parser) advance() Token {
	token := p.peek()
	if token.Kind != TokenEOF {
		p.current++
	}
	return token
}

func (p *parser) errorAt(token Token, message string) {
	p.diagnostics = append(p.diagnostics, Diagnostic{Span: token.Span, Message: message})
}

func (p *parser) synchronizeStatement() {
	for !p.at(TokenEOF) && !p.at(TokenRBrace) {
		if p.match(TokenSemicolon) {
			return
		}
		p.advance()
	}
}

func (p *parser) synchronizeTopLevel() {
	for !p.at(TokenEOF) {
		if p.at(TokenGoal) || p.at(TokenFlow) || p.at(TokenForeach) {
			return
		}
		p.advance()
	}
}
