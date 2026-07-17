package language

type Program struct {
	Loops []Loop `json:"loops"`
}

type Node interface {
	NodeSpan() Span
}

type Loop interface {
	Node
	loopNode()
	LoopName() string
}

type Goal struct {
	Span       Span        `json:"span"`
	Name       string      `json:"name,omitempty"`
	Statements []Statement `json:"statements"`
}

func (g *Goal) NodeSpan() Span   { return g.Span }
func (g *Goal) loopNode()        {}
func (g *Goal) LoopName() string { return g.Name }

type Flow struct {
	Span       Span        `json:"span"`
	Name       string      `json:"name,omitempty"`
	Statements []Statement `json:"statements"`
}

func (f *Flow) NodeSpan() Span   { return f.Span }
func (f *Flow) loopNode()        {}
func (f *Flow) LoopName() string { return f.Name }

type Foreach struct {
	Span     Span   `json:"span"`
	Name     string `json:"name,omitempty"`
	Binding  string `json:"binding"`
	Sequence Expr   `json:"sequence"`
	Body     Loop   `json:"body"`
}

func (f *Foreach) NodeSpan() Span   { return f.Span }
func (f *Foreach) loopNode()        {}
func (f *Foreach) LoopName() string { return f.Name }

type Statement interface {
	Node
	statementNode()
}

type Step struct {
	Span  Span   `json:"span"`
	Name  string `json:"name"`
	Value Value  `json:"value"`
}

func (s *Step) NodeSpan() Span { return s.Span }
func (s *Step) statementNode() {}

type Verify struct {
	Span  Span   `json:"span"`
	Name  string `json:"name"`
	Check Expr   `json:"check"`
}

func (v *Verify) NodeSpan() Span { return v.Span }
func (v *Verify) statementNode() {}

type Limit struct {
	Span  Span   `json:"span"`
	Kind  string `json:"kind"`
	Value Expr   `json:"value"`
}

func (l *Limit) NodeSpan() Span { return l.Span }
func (l *Limit) statementNode() {}

type Return struct {
	Span  Span `json:"span"`
	Value Expr `json:"value"`
}

func (r *Return) NodeSpan() Span { return r.Span }
func (r *Return) statementNode() {}

type Policy struct {
	Span  Span `json:"span"`
	Allow bool `json:"allow"`
	Value Expr `json:"value"`
}

func (p *Policy) NodeSpan() Span { return p.Span }
func (p *Policy) statementNode() {}

type Value interface {
	Node
	valueNode()
}

type Expr interface {
	Value
	exprNode()
}

type Identifier struct {
	Span Span   `json:"span"`
	Name string `json:"name"`
}

func (i *Identifier) NodeSpan() Span { return i.Span }
func (i *Identifier) valueNode()     {}
func (i *Identifier) exprNode()      {}

type Literal struct {
	Span  Span   `json:"span"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func (l *Literal) NodeSpan() Span { return l.Span }
func (l *Literal) valueNode()     {}
func (l *Literal) exprNode()      {}

type Member struct {
	Span     Span   `json:"span"`
	Receiver Expr   `json:"receiver"`
	Name     string `json:"name"`
}

func (m *Member) NodeSpan() Span { return m.Span }
func (m *Member) valueNode()     {}
func (m *Member) exprNode()      {}

type CallArgument struct {
	Span  Span   `json:"span"`
	Name  string `json:"name,omitempty"`
	Value Expr   `json:"value"`
}

type Call struct {
	Span      Span           `json:"span"`
	Callee    Expr           `json:"callee"`
	Arguments []CallArgument `json:"arguments"`
}

func (c *Call) NodeSpan() Span { return c.Span }
func (c *Call) valueNode()     {}
func (c *Call) exprNode()      {}

type Binary struct {
	Span     Span      `json:"span"`
	Left     Expr      `json:"left"`
	Operator TokenKind `json:"operator"`
	Right    Expr      `json:"right"`
}

func (b *Binary) NodeSpan() Span { return b.Span }
func (b *Binary) valueNode()     {}
func (b *Binary) exprNode()      {}

type Unary struct {
	Span     Span      `json:"span"`
	Operator TokenKind `json:"operator"`
	Value    Expr      `json:"value"`
}

func (u *Unary) NodeSpan() Span { return u.Span }
func (u *Unary) valueNode()     {}
func (u *Unary) exprNode()      {}

type List struct {
	Span   Span   `json:"span"`
	Values []Expr `json:"values"`
}

func (l *List) NodeSpan() Span { return l.Span }
func (l *List) valueNode()     {}
func (l *List) exprNode()      {}

type LoopValue struct {
	Loop Loop `json:"loop"`
}

func (l *LoopValue) NodeSpan() Span { return l.Loop.NodeSpan() }
func (l *LoopValue) valueNode()     {}
