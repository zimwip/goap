package cypher

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/antlr4-go/antlr/v4"

	"github.com/zimwip/goap/lib/cypher/parser"
)

// Error is a query the subset does not read: a syntax error of the grammar or a construct it leaves out, at a line and
// a column of the query (1-based; 0: the query as a whole).
type Error struct {
	Line, Col int
	Msg       string
}

func (e *Error) Error() string {
	if e.Line == 0 {
		return e.Msg
	}
	return fmt.Sprintf("%d:%d: %s", e.Line, e.Col, e.Msg)
}

// MaxQueryBytes is the longest query Parse reads.
const MaxQueryBytes = 16 << 10

// Parse reads a query of the subset.
func Parse(src string) (q *Query, err error) {
	if len(src) > MaxQueryBytes {
		return nil, &Error{Msg: fmt.Sprintf("a query is at most %d bytes", MaxQueryBytes)}
	}
	if strings.TrimSpace(src) == "" {
		return nil, &Error{Msg: "empty query"}
	}
	errs := &errorListener{}
	lexer := parser.NewCypherLexer(antlr.NewInputStream(src))
	lexer.RemoveErrorListeners()
	lexer.AddErrorListener(errs)
	p := parser.NewCypherParser(antlr.NewCommonTokenStream(lexer, antlr.TokenDefaultChannel))
	p.RemoveErrorListeners()
	p.AddErrorListener(errs)
	tree := p.OC_Cypher()
	if errs.err != nil {
		return nil, errs.err
	}
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*Error)
			if !ok {
				panic(r)
			}
			q, err = nil, e
		}
	}()
	return buildQuery(tree.OC_Statement().OC_Query()), nil
}

type errorListener struct {
	*antlr.DefaultErrorListener
	err *Error
}

func (l *errorListener) SyntaxError(_ antlr.Recognizer, _ any, line, col int, msg string, _ antlr.RecognitionException) {
	if l.err == nil {
		l.err = &Error{Line: line, Col: col + 1, Msg: msg}
	}
}

// refuse stops the build on a construct the subset leaves out.
func refuse(ctx antlr.ParserRuleContext, what string) {
	t := ctx.GetStart()
	panic(&Error{Line: t.GetLine(), Col: t.GetColumn() + 1, Msg: what + " is not supported: the query language is read-only MATCH … RETURN (ADR 0160)"})
}

func buildQuery(ctx parser.IOC_QueryContext) *Query {
	if ctx.OC_StandaloneCall() != nil {
		refuse(ctx, "CALL")
	}
	rq := ctx.OC_RegularQuery()
	if len(rq.AllOC_Union()) > 0 {
		refuse(rq.OC_Union(0), "UNION")
	}
	sq := rq.OC_SingleQuery()
	if mp := sq.OC_MultiPartQuery(); mp != nil {
		refuse(mp.OC_With(0), "WITH")
	}
	sp := sq.OC_SinglePartQuery()
	if len(sp.AllOC_UpdatingClause()) > 0 {
		refuse(sp.OC_UpdatingClause(0), "a write ("+firstWord(sp.OC_UpdatingClause(0))+")")
	}
	q := &Query{}
	for _, rc := range sp.AllOC_ReadingClause() {
		switch {
		case rc.OC_Unwind() != nil:
			refuse(rc, "UNWIND")
		case rc.OC_InQueryCall() != nil:
			refuse(rc, "CALL")
		}
		m := rc.OC_Match()
		if m.OPTIONAL() != nil {
			refuse(m, "OPTIONAL MATCH")
		}
		match := Match{}
		pat := m.OC_Pattern()
		for _, pp := range pat.AllOC_PatternPart() {
			match.Patterns = append(match.Patterns, buildPatternPart(pp))
		}
		if w := m.OC_Where(); w != nil {
			match.Where = buildExpr(w.OC_Expression())
		}
		q.Matches = append(q.Matches, match)
	}
	if len(q.Matches) == 0 {
		refuse(sp, "a query without MATCH")
	}
	if sp.OC_Return() == nil {
		refuse(sp, "a query without RETURN")
	}
	q.Return = buildReturn(sp.OC_Return().OC_ProjectionBody())
	return q
}

func firstWord(ctx antlr.ParserRuleContext) string {
	return strings.ToUpper(strings.Fields(ctx.GetText() + " ")[0])
}

func buildPatternPart(pp parser.IOC_PatternPartContext) Pattern {
	var p Pattern
	if v := pp.OC_Variable(); v != nil {
		p.Var = symbolic(v.OC_SymbolicName())
	}
	el := pp.OC_AnonymousPatternPart().OC_PatternElement()
	for el.OC_PatternElement() != nil { // ( pattern )
		el = el.OC_PatternElement()
	}
	p.Nodes = append(p.Nodes, buildNode(el.OC_NodePattern()))
	for _, ch := range el.AllOC_PatternElementChain() {
		p.Rels = append(p.Rels, buildRel(ch.OC_RelationshipPattern()))
		p.Nodes = append(p.Nodes, buildNode(ch.OC_NodePattern()))
	}
	return p
}

func buildNode(np parser.IOC_NodePatternContext) NodePattern {
	var n NodePattern
	if v := np.OC_Variable(); v != nil {
		n.Var = symbolic(v.OC_SymbolicName())
	}
	if ls := np.OC_NodeLabels(); ls != nil {
		n.Labels = labels(ls)
	}
	if ps := np.OC_Properties(); ps != nil {
		n.Props = buildProps(ps)
	}
	return n
}

func labels(ls parser.IOC_NodeLabelsContext) []string {
	var out []string
	for _, l := range ls.AllOC_NodeLabel() {
		out = append(out, schemaName(l.OC_LabelName().OC_SchemaName()))
	}
	return out
}

func buildRel(rp parser.IOC_RelationshipPatternContext) RelPattern {
	var r RelPattern
	left, right := rp.OC_LeftArrowHead() != nil, rp.OC_RightArrowHead() != nil
	switch {
	case left && right:
		r.Dir = Either // <-[]-> reads as either direction
	case right:
		r.Dir = Right
	case left:
		r.Dir = Left
	}
	d := rp.OC_RelationshipDetail()
	if d == nil {
		return r
	}
	if v := d.OC_Variable(); v != nil {
		r.Var = symbolic(v.OC_SymbolicName())
	}
	if ts := d.OC_RelationshipTypes(); ts != nil {
		for _, t := range ts.AllOC_RelTypeName() {
			r.Types = append(r.Types, schemaName(t.OC_SchemaName()))
		}
	}
	if rl := d.OC_RangeLiteral(); rl != nil {
		r.Range = buildRange(rl)
	}
	if ps := d.OC_Properties(); ps != nil {
		r.Props = buildProps(ps)
	}
	return r
}

// buildRange reads `*`, `*n`, `*n..`, `*..m`, `*n..m`.
func buildRange(rl parser.IOC_RangeLiteralContext) *Range {
	ints := rl.AllOC_IntegerLiteral()
	dots := strings.Contains(rl.GetText(), "..")
	r := &Range{Min: 1}
	switch {
	case len(ints) == 2:
		r.Min, r.Max = intOf(ints[0]), intOf(ints[1])
	case len(ints) == 1 && !dots:
		r.Min = intOf(ints[0])
		r.Max = r.Min
	case len(ints) == 1:
		// `*n..` or `*..m`: the integer is the bound on its side of the dots
		if strings.Index(rl.GetText(), "..") < strings.Index(rl.GetText(), ints[0].GetText()) {
			r.Max = intOf(ints[0])
		} else {
			r.Min = intOf(ints[0])
		}
	}
	if r.Max != 0 && r.Max < r.Min {
		refuse(rl, fmt.Sprintf("a range from %d down to %d", r.Min, r.Max))
	}
	return r
}

func intOf(il parser.IOC_IntegerLiteralContext) int {
	v, ok := integer(il)
	if !ok || v < 0 || v > 1<<20 {
		refuse(il, "the integer "+il.GetText())
	}
	return int(v)
}

func integer(il parser.IOC_IntegerLiteralContext) (int64, bool) {
	t := il.GetText()
	var v int64
	var err error
	switch {
	case il.HexInteger() != nil:
		v, err = strconv.ParseInt(t[2:], 16, 64)
	case il.OctalInteger() != nil:
		v, err = strconv.ParseInt(t[2:], 8, 64)
	default:
		v, err = strconv.ParseInt(t, 10, 64)
	}
	return v, err == nil
}

func buildProps(ps parser.IOC_PropertiesContext) map[string]Expr {
	if ps.OC_Parameter() != nil {
		refuse(ps, "a parameter")
	}
	m := buildMap(ps.OC_MapLiteral())
	out := make(map[string]Expr, len(m.Keys))
	for i, k := range m.Keys {
		out[k] = m.Values[i]
	}
	return out
}

func buildMap(ml parser.IOC_MapLiteralContext) Map {
	var m Map
	for i, k := range ml.AllOC_PropertyKeyName() {
		m.Keys = append(m.Keys, schemaName(k.OC_SchemaName()))
		m.Values = append(m.Values, buildExpr(ml.OC_Expression(i)))
	}
	return m
}

func buildReturn(pb parser.IOC_ProjectionBodyContext) Return {
	r := Return{Distinct: pb.DISTINCT() != nil}
	items := pb.OC_ProjectionItems()
	r.Star = strings.HasPrefix(strings.TrimSpace(items.GetText()), "*")
	for _, it := range items.AllOC_ProjectionItem() {
		item := Item{Expr: buildExpr(it.OC_Expression()), Name: strings.TrimSpace(it.OC_Expression().GetText())}
		if v := it.OC_Variable(); v != nil {
			item.Name = symbolic(v.OC_SymbolicName())
		} else {
			item.Name = columnName(it.OC_Expression())
		}
		r.Items = append(r.Items, item)
	}
	if o := pb.OC_Order(); o != nil {
		for _, si := range o.AllOC_SortItem() {
			r.Order = append(r.Order, SortItem{Expr: buildExpr(si.OC_Expression()), Desc: si.DESC() != nil || si.DESCENDING() != nil})
		}
	}
	if s := pb.OC_Skip(); s != nil {
		r.Skip = buildExpr(s.OC_Expression())
	}
	if l := pb.OC_Limit(); l != nil {
		r.Limit = buildExpr(l.OC_Expression())
	}
	return r
}

// columnName is the text of an expression as written (its source, spaces kept).
func columnName(e parser.IOC_ExpressionContext) string {
	start, stop := e.GetStart(), e.GetStop()
	if start == nil || stop == nil {
		return e.GetText()
	}
	if s := start.GetInputStream(); s != nil {
		return s.GetText(start.GetStart(), stop.GetStop())
	}
	return e.GetText()
}

// buildExpr turns an expression of the grammar into the tree, refusing what the subset leaves out.
func buildExpr(e parser.IOC_ExpressionContext) Expr {
	return buildOr(e.OC_OrExpression())
}

func buildOr(o parser.IOC_OrExpressionContext) Expr {
	xs := o.AllOC_XorExpression()
	out := buildXor(xs[0])
	for _, x := range xs[1:] {
		out = Binary{Op: "OR", L: out, R: buildXor(x)}
	}
	return out
}

func buildXor(o parser.IOC_XorExpressionContext) Expr {
	xs := o.AllOC_AndExpression()
	out := buildAnd(xs[0])
	for _, x := range xs[1:] {
		out = Binary{Op: "XOR", L: out, R: buildAnd(x)}
	}
	return out
}

func buildAnd(o parser.IOC_AndExpressionContext) Expr {
	xs := o.AllOC_NotExpression()
	out := buildNot(xs[0])
	for _, x := range xs[1:] {
		out = Binary{Op: "AND", L: out, R: buildNot(x)}
	}
	return out
}

func buildNot(n parser.IOC_NotExpressionContext) Expr {
	out := buildComparison(n.OC_ComparisonExpression())
	for range n.AllNOT() {
		out = Unary{Op: "NOT", X: out}
	}
	return out
}

// buildComparison reads `a < b <= c` as `a < b AND b <= c`.
func buildComparison(c parser.IOC_ComparisonExpressionContext) Expr {
	left := buildPredicate(c.OC_StringListNullPredicateExpression())
	var out Expr
	for _, pc := range c.AllOC_PartialComparisonExpression() {
		right := buildPredicate(pc.OC_StringListNullPredicateExpression())
		cmp := Binary{Op: operator(pc, "=", "<>", "<=", ">=", "<", ">"), L: left, R: right}
		if out == nil {
			out = cmp
		} else {
			out = Binary{Op: "AND", L: out, R: cmp}
		}
		left = right
	}
	if out == nil {
		return left
	}
	return out
}

func buildPredicate(p parser.IOC_StringListNullPredicateExpressionContext) Expr {
	out := buildAdd(p.OC_AddOrSubtractExpression())
	for _, ch := range p.GetChildren()[1:] {
		switch x := ch.(type) {
		case parser.IOC_StringPredicateExpressionContext:
			op := "CONTAINS"
			if x.STARTS() != nil {
				op = "STARTS WITH"
			} else if x.ENDS() != nil {
				op = "ENDS WITH"
			}
			out = Binary{Op: op, L: out, R: buildAdd(x.OC_AddOrSubtractExpression())}
		case parser.IOC_ListPredicateExpressionContext:
			out = Binary{Op: "IN", L: out, R: buildAdd(x.OC_AddOrSubtractExpression())}
		case parser.IOC_NullPredicateExpressionContext:
			out = IsNull{X: out, Not: x.NOT() != nil}
		}
	}
	return out
}

func buildAdd(a parser.IOC_AddOrSubtractExpressionContext) Expr {
	return binaryChain(a, func(ch antlr.Tree) (Expr, bool) {
		x, ok := ch.(parser.IOC_MultiplyDivideModuloExpressionContext)
		if !ok {
			return nil, false
		}
		return buildMul(x), true
	})
}

func buildMul(m parser.IOC_MultiplyDivideModuloExpressionContext) Expr {
	return binaryChain(m, func(ch antlr.Tree) (Expr, bool) {
		x, ok := ch.(parser.IOC_PowerOfExpressionContext)
		if !ok {
			return nil, false
		}
		return buildPow(x), true
	})
}

func buildPow(p parser.IOC_PowerOfExpressionContext) Expr {
	return binaryChain(p, func(ch antlr.Tree) (Expr, bool) {
		x, ok := ch.(parser.IOC_UnaryAddOrSubtractExpressionContext)
		if !ok {
			return nil, false
		}
		return buildUnary(x), true
	})
}

// binaryChain reads `x op y op z` left to right: the operands are the children operand reads, the operators the
// terminal children between them.
func binaryChain(ctx antlr.ParserRuleContext, operand func(antlr.Tree) (Expr, bool)) Expr {
	var out Expr
	op := ""
	for _, ch := range ctx.GetChildren() {
		if e, ok := operand(ch); ok {
			if out == nil {
				out = e
			} else {
				out = Binary{Op: op, L: out, R: e}
			}
			continue
		}
		if t, ok := ch.(antlr.TerminalNode); ok {
			if s := strings.TrimSpace(t.GetText()); s != "" {
				op = s
			}
		}
	}
	return out
}

func buildUnary(u parser.IOC_UnaryAddOrSubtractExpressionContext) Expr {
	x := buildNonArithmetic(u.OC_NonArithmeticOperatorExpression())
	if t, ok := u.GetChild(0).(antlr.TerminalNode); ok && t.GetText() == "-" {
		return Unary{Op: "-", X: x}
	}
	return x
}

func buildNonArithmetic(n parser.IOC_NonArithmeticOperatorExpressionContext) Expr {
	out := buildAtom(n.OC_Atom())
	for _, ch := range n.GetChildren()[1:] {
		switch x := ch.(type) {
		case parser.IOC_PropertyLookupContext:
			out = Prop{X: out, Key: schemaName(x.OC_PropertyKeyName().OC_SchemaName())}
		case parser.IOC_ListOperatorExpressionContext:
			out = buildListOperator(out, x)
		case parser.IOC_NodeLabelsContext:
			out = HasLabels{X: out, Labels: labels(x)}
		}
	}
	return out
}

func buildListOperator(x Expr, lo parser.IOC_ListOperatorExpressionContext) Expr {
	dots := false
	var from, to Expr
	for _, ch := range lo.GetChildren() {
		switch c := ch.(type) {
		case antlr.TerminalNode:
			if c.GetText() == ".." {
				dots = true
			}
		case parser.IOC_ExpressionContext:
			if dots {
				to = buildExpr(c)
			} else {
				from = buildExpr(c)
			}
		}
	}
	if !dots {
		return Index{X: x, I: from}
	}
	return Slice{X: x, From: from, To: to}
}

func buildAtom(a parser.IOC_AtomContext) Expr {
	switch {
	case a.OC_Literal() != nil:
		return buildLiteral(a.OC_Literal())
	case a.OC_Parameter() != nil:
		refuse(a, "a parameter")
	case a.OC_CaseExpression() != nil:
		return buildCase(a.OC_CaseExpression())
	case a.COUNT() != nil:
		return Call{Name: "count", Star: true}
	case a.OC_ListComprehension() != nil, a.OC_PatternComprehension() != nil:
		refuse(a, "a list comprehension")
	case a.OC_Quantifier() != nil:
		refuse(a, "a quantifier (all, any, none, single)")
	case a.OC_PatternPredicate() != nil, a.OC_ExistentialSubquery() != nil:
		refuse(a, "a pattern in an expression")
	case a.OC_ParenthesizedExpression() != nil:
		return buildExpr(a.OC_ParenthesizedExpression().OC_Expression())
	case a.OC_FunctionInvocation() != nil:
		return buildCall(a.OC_FunctionInvocation())
	case a.OC_Variable() != nil:
		return Var{Name: symbolic(a.OC_Variable().OC_SymbolicName())}
	}
	refuse(a, "the expression "+a.GetText())
	return nil
}

func buildCall(f parser.IOC_FunctionInvocationContext) Expr {
	name := f.OC_FunctionName()
	if ns := name.OC_Namespace(); len(ns.AllOC_SymbolicName()) > 0 {
		refuse(f, "the function "+name.GetText())
	}
	c := Call{Name: strings.ToLower(symbolic(name.OC_SymbolicName())), Distinct: f.DISTINCT() != nil}
	if _, ok := functions[c.Name]; !ok && !aggregates[c.Name] {
		refuse(f, "the function "+name.GetText())
	}
	for _, e := range f.AllOC_Expression() {
		c.Args = append(c.Args, buildExpr(e))
	}
	return c
}

func buildCase(ce parser.IOC_CaseExpressionContext) Expr {
	var c Case
	sawAlt, sawElse := false, false
	for _, ch := range ce.GetChildren() {
		switch x := ch.(type) {
		case antlr.TerminalNode:
			if strings.EqualFold(x.GetText(), "ELSE") {
				sawElse = true
			}
		case parser.IOC_CaseAlternativeContext:
			sawAlt = true
			c.Whens = append(c.Whens, buildExpr(x.OC_Expression(0)))
			c.Thens = append(c.Thens, buildExpr(x.OC_Expression(1)))
		case parser.IOC_ExpressionContext:
			switch {
			case sawElse:
				c.Else = buildExpr(x)
			case !sawAlt:
				c.Subject = buildExpr(x)
			}
		}
	}
	return c
}

func buildLiteral(l parser.IOC_LiteralContext) Expr {
	switch {
	case l.NULL() != nil:
		return Lit{}
	case l.OC_BooleanLiteral() != nil:
		return Lit{Value: l.OC_BooleanLiteral().TRUE() != nil}
	case l.OC_NumberLiteral() != nil:
		n := l.OC_NumberLiteral()
		if il := n.OC_IntegerLiteral(); il != nil {
			v, ok := integer(il)
			if !ok {
				refuse(il, "the integer "+il.GetText())
			}
			return Lit{Value: float64(v)}
		}
		v, err := strconv.ParseFloat(n.GetText(), 64)
		if err != nil {
			refuse(n, "the number "+n.GetText())
		}
		return Lit{Value: v}
	case l.StringLiteral() != nil:
		return Lit{Value: unquote(l.StringLiteral().GetText())}
	case l.OC_ListLiteral() != nil:
		var list List
		for _, e := range l.OC_ListLiteral().AllOC_Expression() {
			list.Items = append(list.Items, buildExpr(e))
		}
		return list
	case l.OC_MapLiteral() != nil:
		return buildMap(l.OC_MapLiteral())
	}
	refuse(l, "the literal "+l.GetText())
	return nil
}

// unquote reads a string literal: its quotes dropped and its escapes read.
func unquote(s string) string {
	s = s[1 : len(s)-1]
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'b', 'B':
			b.WriteByte('\b')
		case 'f', 'F':
			b.WriteByte('\f')
		case 'n', 'N':
			b.WriteByte('\n')
		case 'r', 'R':
			b.WriteByte('\r')
		case 't', 'T':
			b.WriteByte('\t')
		case 'u', 'U':
			n := 4
			if i+9 <= len(s) && isHex(s[i+1:i+9]) {
				n = 8
			}
			if i+1+n <= len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32); err == nil && utf8.ValidRune(rune(r)) {
					b.WriteRune(rune(r))
					i += n
					continue
				}
			}
			b.WriteByte(s[i])
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// symbolic reads a name: `escaped` names lose their backticks (a doubled one is one).
func symbolic(s parser.IOC_SymbolicNameContext) string {
	t := s.GetText()
	if s.EscapedSymbolicName() != nil {
		return strings.ReplaceAll(t[1:len(t)-1], "``", "`")
	}
	return t
}

func schemaName(s parser.IOC_SchemaNameContext) string {
	if s.OC_SymbolicName() != nil {
		return symbolic(s.OC_SymbolicName())
	}
	return s.GetText()
}

// operator is the first of ops a context's terminal children spell.
func operator(ctx antlr.ParserRuleContext, ops ...string) string {
	for _, ch := range ctx.GetChildren() {
		if t, ok := ch.(antlr.TerminalNode); ok {
			s := strings.TrimSpace(t.GetText())
			for _, op := range ops {
				if s == op {
					return op
				}
			}
		}
	}
	return ops[0]
}
