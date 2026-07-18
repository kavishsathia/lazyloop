package language

import (
	"fmt"
	"sort"
)

type Position struct {
	Offset int `json:"offset"`
	Line   int `json:"line"`
	Column int `json:"column"`
}

type Span struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

func (s Span) String() string {
	return fmt.Sprintf("%d:%d", s.Start.Line, s.Start.Column)
}

type TokenKind string

const (
	TokenEOF        TokenKind = "eof"
	TokenIdentifier TokenKind = "identifier"
	TokenString     TokenKind = "string"
	TokenNumber     TokenKind = "number"
	TokenGoal       TokenKind = "goal"
	TokenFlow       TokenKind = "flow"
	TokenForeach    TokenKind = "foreach"
	TokenStep       TokenKind = "step"
	TokenVerify     TokenKind = "verify"
	TokenLimit      TokenKind = "limit"
	TokenReturn     TokenKind = "return"
	TokenAllow      TokenKind = "allow"
	TokenDisallow   TokenKind = "disallow"
	TokenIn         TokenKind = "in"
	TokenWith       TokenKind = "with"
	TokenIf         TokenKind = "if"
	TokenFork       TokenKind = "fork"
	TokenExec       TokenKind = "exec"
	TokenWait       TokenKind = "wait"
	TokenTrue       TokenKind = "true"
	TokenFalse      TokenKind = "false"
	TokenLBrace     TokenKind = "{"
	TokenRBrace     TokenKind = "}"
	TokenLParen     TokenKind = "("
	TokenRParen     TokenKind = ")"
	TokenLBracket   TokenKind = "["
	TokenRBracket   TokenKind = "]"
	TokenComma      TokenKind = ","
	TokenSemicolon  TokenKind = ";"
	TokenDot        TokenKind = "."
	TokenColon      TokenKind = ":"
	TokenAssign     TokenKind = "="
	TokenEqual      TokenKind = "=="
	TokenNotEqual   TokenKind = "!="
	TokenLess       TokenKind = "<"
	TokenLessEqual  TokenKind = "<="
	TokenGreater    TokenKind = ">"
	TokenGreaterEq  TokenKind = ">="
	TokenAnd        TokenKind = "&&"
	TokenOr         TokenKind = "||"
	TokenBang       TokenKind = "!"
)

type Token struct {
	Kind   TokenKind
	Lexeme string
	Span   Span
}

var keywords = map[string]TokenKind{
	"goal":     TokenGoal,
	"flow":     TokenFlow,
	"foreach":  TokenForeach,
	"step":     TokenStep,
	"verify":   TokenVerify,
	"limit":    TokenLimit,
	"return":   TokenReturn,
	"allow":    TokenAllow,
	"disallow": TokenDisallow,
	"in":       TokenIn,
	"with":     TokenWith,
	"if":       TokenIf,
	"fork":     TokenFork,
	"exec":     TokenExec,
	"wait":     TokenWait,
	"true":     TokenTrue,
	"false":    TokenFalse,
}

// Keywords returns the language's reserved words in stable lexical order.
// Reference validation uses this as the source of truth for documentation coverage.
func Keywords() []string {
	result := make([]string, 0, len(keywords))
	for keyword := range keywords {
		result = append(result, keyword)
	}
	sort.Strings(result)
	return result
}
