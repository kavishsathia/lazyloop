package language

import "fmt"

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
	"true":     TokenTrue,
	"false":    TokenFalse,
}
