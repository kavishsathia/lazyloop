package language

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

type lexer struct {
	source      string
	offset      int
	line        int
	column      int
	diagnostics Diagnostics
}

func Lex(source string) ([]Token, Diagnostics) {
	l := &lexer{source: source, line: 1, column: 1}
	var tokens []Token
	for {
		l.skipWhitespaceAndComments()
		start := l.position()
		if l.offset >= len(l.source) {
			tokens = append(tokens, Token{Kind: TokenEOF, Span: Span{Start: start, End: start}})
			return tokens, l.diagnostics
		}

		r, _ := l.peek()
		switch {
		case isIdentifierStart(r):
			tokens = append(tokens, l.lexIdentifier(start))
		case unicode.IsDigit(r):
			tokens = append(tokens, l.lexNumber(start))
		case r == '"':
			tokens = append(tokens, l.lexString(start))
		default:
			if token, ok := l.lexPunctuation(start); ok {
				tokens = append(tokens, token)
			} else {
				l.advance()
				l.diagnostics = append(l.diagnostics, Diagnostic{
					Span:    Span{Start: start, End: l.position()},
					Message: fmt.Sprintf("unexpected character %q", r),
				})
			}
		}
	}
}

func (l *lexer) lexIdentifier(start Position) Token {
	begin := l.offset
	for {
		r, ok := l.peek()
		if !ok || !isIdentifierContinue(r) {
			break
		}
		l.advance()
	}
	lexeme := l.source[begin:l.offset]
	kind := TokenIdentifier
	if keyword, ok := keywords[lexeme]; ok {
		kind = keyword
	}
	return Token{Kind: kind, Lexeme: lexeme, Span: Span{Start: start, End: l.position()}}
}

func (l *lexer) lexNumber(start Position) Token {
	begin := l.offset
	seenDot := false
	for {
		r, ok := l.peek()
		if !ok {
			break
		}
		if unicode.IsDigit(r) || unicode.IsLetter(r) || r == '_' {
			l.advance()
			continue
		}
		if r == '.' && !seenDot {
			seenDot = true
			l.advance()
			continue
		}
		break
	}
	return Token{Kind: TokenNumber, Lexeme: l.source[begin:l.offset], Span: Span{Start: start, End: l.position()}}
}

func (l *lexer) lexString(start Position) Token {
	begin := l.offset
	l.advance()
	escaped := false
	for {
		r, ok := l.peek()
		if !ok || (!escaped && r == '\n') {
			l.diagnostics = append(l.diagnostics, Diagnostic{Span: Span{Start: start, End: l.position()}, Message: "unterminated string"})
			return Token{Kind: TokenString, Lexeme: l.source[begin:l.offset], Span: Span{Start: start, End: l.position()}}
		}
		l.advance()
		if !escaped && r == '"' {
			return Token{Kind: TokenString, Lexeme: l.source[begin:l.offset], Span: Span{Start: start, End: l.position()}}
		}
		if !escaped && r == '\\' {
			escaped = true
		} else {
			escaped = false
		}
	}
}

func (l *lexer) lexPunctuation(start Position) (Token, bool) {
	begin := l.offset
	r, _ := l.peek()
	l.advance()
	kind, ok := map[rune]TokenKind{
		'{': TokenLBrace, '}': TokenRBrace, '(': TokenLParen, ')': TokenRParen,
		'[': TokenLBracket, ']': TokenRBracket, ',': TokenComma, ';': TokenSemicolon,
		'.': TokenDot, ':': TokenColon,
	}[r]
	if ok {
		return Token{Kind: kind, Lexeme: l.source[begin:l.offset], Span: Span{Start: start, End: l.position()}}, true
	}

	next, hasNext := l.peek()
	switch r {
	case '=':
		kind = TokenAssign
		if hasNext && next == '=' {
			l.advance()
			kind = TokenEqual
		}
	case '!':
		kind = TokenBang
		if hasNext && next == '=' {
			l.advance()
			kind = TokenNotEqual
		}
	case '<':
		kind = TokenLess
		if hasNext && next == '=' {
			l.advance()
			kind = TokenLessEqual
		}
	case '>':
		kind = TokenGreater
		if hasNext && next == '=' {
			l.advance()
			kind = TokenGreaterEq
		}
	case '&':
		if hasNext && next == '&' {
			l.advance()
			kind = TokenAnd
		} else {
			return Token{}, false
		}
	case '|':
		if hasNext && next == '|' {
			l.advance()
			kind = TokenOr
		} else {
			return Token{}, false
		}
	default:
		return Token{}, false
	}
	return Token{Kind: kind, Lexeme: l.source[begin:l.offset], Span: Span{Start: start, End: l.position()}}, true
}

func (l *lexer) skipWhitespaceAndComments() {
	for {
		r, ok := l.peek()
		if !ok {
			return
		}
		if unicode.IsSpace(r) {
			l.advance()
			continue
		}
		if r == '/' && l.peekByte(1) == '/' {
			for {
				r, ok = l.peek()
				if !ok || r == '\n' {
					break
				}
				l.advance()
			}
			continue
		}
		return
	}
}

func (l *lexer) peek() (rune, bool) {
	if l.offset >= len(l.source) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(l.source[l.offset:])
	return r, true
}

func (l *lexer) peekByte(ahead int) byte {
	if l.offset+ahead >= len(l.source) {
		return 0
	}
	return l.source[l.offset+ahead]
}

func (l *lexer) advance() {
	r, size := utf8.DecodeRuneInString(l.source[l.offset:])
	l.offset += size
	if r == '\n' {
		l.line++
		l.column = 1
	} else {
		l.column++
	}
}

func (l *lexer) position() Position {
	return Position{Offset: l.offset, Line: l.line, Column: l.column}
}

func isIdentifierStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isIdentifierContinue(r rune) bool {
	return isIdentifierStart(r) || unicode.IsDigit(r) || r == '-'
}
