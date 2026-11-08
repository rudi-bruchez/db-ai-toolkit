// Package showplan reads SQL Server showplan XML into the structures goDTA
// needs: per statement, the objects read and how, the writes and their own
// cost, the compiled parameter values, the missing index groups, the sorts and
// the operator tree (spec sections 8.3 and 8.5).
//
// The reader streams the document with encoding/xml's Decoder.Token, so a very
// large plan is never held as a tree of tokens. It makes no assumption on
// self-closing elements: Query Store plans close ColumnReference and Object
// with an end tag (measured).
package showplan

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlplan"
)

// Plan is one showplan document.
type Plan struct {
	// Statements lists every statement element in document order, nested
	// ones included: the EXEC of a procedure comes first, without a plan
	// (Root nil), then the statements of the procedure.
	Statements []Statement
}

// Statement is one statement of the plan.
type Statement struct {
	Text, Type     string
	Cost           float64 // StatementSubTreeCost
	CompileTimeout bool    // StatementOptmEarlyAbortReason="TimeOut"
	// Params come from the ParameterList child of QueryPlan only, in XML
	// order, which is the reverse of the declaration: match them by name.
	Params   []Param
	Accesses []Access
	Writes   []Write
	Missing  []MissingIndex
	Sorts    []SortReq
	// JoinCols are the table columns a join compares: the hash keys and
	// probe residual of a Hash Match join or an Adaptive Join, the join
	// columns and residual of a Merge Join, the outer references and the
	// predicate of a Nested Loops. GroupCols are the table columns a Stream
	// Aggregate groups by, or a Hash Match aggregate hashes on (it has no
	// GroupBy element, measured on 2022). Computed values (Expr1001) and
	// bookmarks name no table and are left out; each column comes once.
	JoinCols, GroupCols []ColRef
	Parallel            bool // some operator runs in parallel
	Root                *RelOp
}

// Param is a parameter as compiled, CompiledValue as written in the XML once
// unescaped: (9332), 'Martin', N'x', NULL.
type Param struct {
	Name, DataType, CompiledValue string
}

// ObjRef names an object of the plan, brackets removed.
type ObjRef struct {
	Database, Schema, Table, Index, Alias string
	IndexKind                             string
}

// ColRef names a column of the plan, brackets removed.
type ColRef struct {
	Database, Schema, Table, Alias, Column string
}

// Access is one read of a table, an index or an indexed view.
type Access struct {
	Obj ObjRef
	// ViewBase lists the base tables of a substituted indexed view, which
	// appear nowhere else in the plan.
	ViewBase []ObjRef
	Op       string // PhysicalOp
	SeekCols []ColRef
	SeekEq   []bool // per SeekCols: equality (true) or range (false)
	// PredEq and PredRange are the columns of the residual predicate compared
	// by EQ, or by LT, LE, GT, GE, to a constant, a parameter or a variable.
	PredEq, PredRange []ColRef
	Output            []ColRef
	// LookupOf is the index in Accesses of the read a Key Lookup or RID
	// Lookup completes, -1 otherwise.
	LookupOf     int
	EstimateRows float64
}

// Write is one insert, update, delete or merge operator.
type Write struct {
	Op string
	// Objs lists every Object of the operator: a narrow plan has one
	// clustered operator that lists each index it maintains.
	Objs []ObjRef
	// OwnCost is the operator's subtree cost minus that of its children.
	OwnCost float64
	// SetCols are the table columns the operator assigns.
	SetCols []ColRef
}

// MissingIndex is one missing index group.
type MissingIndex struct {
	Impact                        float64
	Obj                           ObjRef
	Equality, Inequality, Include []string
}

// SortReq is the order a Sort or TopN Sort operator asks for.
type SortReq struct {
	Cols []ColRef
	Desc []bool
}

// RelOp is one operator of the tree.
type RelOp struct {
	PhysicalOp, LogicalOp     string
	Obj                       *ObjRef // first Object of the operator
	Children                  []*RelOp
	EstimateRows, SubtreeCost float64
}

// Parse reads a showplan document, UTF-8 or UTF-16 with BOM.
func Parse(data []byte) (*Plan, error) {
	text, engine, err := sqlplan.Decode(data)
	if err != nil {
		return nil, err
	}
	if engine != sqlplan.SQLServer {
		return nil, errors.New("not a SQL Server showplan")
	}
	d := xml.NewDecoder(strings.NewReader(text))
	// The text is already decoded: a declaration naming utf-16 describes
	// the file, not this string.
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	p := &parser{plan: &Plan{}}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the plan: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			p.start(t)
		case xml.EndElement:
			p.end()
		}
	}
	if len(p.stack) != 0 {
		return nil, errors.New("truncated plan")
	}
	return p.plan, nil
}

type frame struct {
	name     string
	children int // element children seen so far
}

type operand struct {
	depth int
	kind  int // opOther, opColumn, opValue
	col   ColRef
	seen  bool
}

const (
	opOther = iota
	opColumn
	opValue
)

type compare struct {
	depth    int
	op       string
	operands []*operand
}

type relState struct {
	node      *RelOp
	depth     int
	opDepth   int // depth of the operator element, 0 until seen
	opName    string
	access    int // index in Accesses, -1
	write     int // index in Writes, -1
	lookup    bool
	output    []ColRef
	outDepth  int
	seekDepth int
	seekEq    bool
	predDepth int
	setDepth  int
	sortIdx   int // index in Sorts, -1
	ivDepth   int // IndexedViewInfo
	joinDepth int // a child of the operator that lists join columns
	grpDepth  int // a child of the operator that lists grouping columns
}

type stmtState struct {
	idx       int
	depth     int
	qpDepth   int
	plDepth   int // ParameterList child of QueryPlan
	rels      []*relState
	all       []*relState // every operator, for the lookup pass
	compares  []*compare
	missing   int // index in Missing of the open group, -1
	misDepth  int
	usage     string
	accessOf  map[*RelOp]int
	lookupOps map[*RelOp]bool
}

type parser struct {
	plan  *Plan
	stack []frame
	stmts []*stmtState
}

func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '[' && s[len(s)-1] == ']' {
		return strings.ReplaceAll(s[1:len(s)-1], "]]", "]")
	}
	return s
}

func objRef(t xml.StartElement) ObjRef {
	return ObjRef{
		Database:  unquote(attr(t, "Database")),
		Schema:    unquote(attr(t, "Schema")),
		Table:     unquote(attr(t, "Table")),
		Index:     unquote(attr(t, "Index")),
		Alias:     unquote(attr(t, "Alias")),
		IndexKind: attr(t, "IndexKind"),
	}
}

func colRef(t xml.StartElement) ColRef {
	return ColRef{
		Database: unquote(attr(t, "Database")),
		Schema:   unquote(attr(t, "Schema")),
		Table:    unquote(attr(t, "Table")),
		Alias:    unquote(attr(t, "Alias")),
		Column:   unquote(attr(t, "Column")),
	}
}

func isWrite(op string) bool {
	for _, prefix := range []string{"Index ", "Clustered Index ", "Table "} {
		if rest, ok := strings.CutPrefix(op, prefix); ok {
			switch rest {
			case "Insert", "Update", "Delete", "Merge":
				return true
			}
		}
	}
	return false
}

// rel returns the innermost operator of the current statement, or nil.
func (s *stmtState) rel() *relState {
	if len(s.rels) == 0 {
		return nil
	}
	return s.rels[len(s.rels)-1]
}

func (p *parser) stmt() *Statement {
	return &p.plan.Statements[p.stmts[len(p.stmts)-1].idx]
}

func (p *parser) start(t xml.StartElement) {
	d := len(p.stack) // depth of this element; its parent is p.stack[d-1]
	parent, childIdx := "", 0
	if d > 0 {
		parent = p.stack[d-1].name
		childIdx = p.stack[d-1].children
		p.stack[d-1].children++
	}
	n := t.Name.Local
	p.stack = append(p.stack, frame{name: n})

	if strings.HasPrefix(n, "Stmt") && parent == "Statements" {
		p.plan.Statements = append(p.plan.Statements, Statement{
			Text:           attr(t, "StatementText"),
			Type:           attr(t, "StatementType"),
			Cost:           num(attr(t, "StatementSubTreeCost")),
			CompileTimeout: attr(t, "StatementOptmEarlyAbortReason") == "TimeOut",
		})
		p.stmts = append(p.stmts, &stmtState{idx: len(p.plan.Statements) - 1, depth: d, missing: -1,
			accessOf: map[*RelOp]int{}, lookupOps: map[*RelOp]bool{}})
		return
	}
	if len(p.stmts) == 0 {
		return
	}
	ss := p.stmts[len(p.stmts)-1]
	st := p.stmt()

	switch {
	case n == "QueryPlan" && d == ss.depth+1:
		ss.qpDepth = d
		return
	case n == "ParameterList" && ss.qpDepth > 0 && d == ss.qpDepth+1:
		ss.plDepth = d
		return
	case n == "ColumnReference" && ss.plDepth > 0 && d == ss.plDepth+1:
		st.Params = append(st.Params, Param{
			Name:          attr(t, "Column"),
			DataType:      attr(t, "ParameterDataType"),
			CompiledValue: attr(t, "ParameterCompiledValue"),
		})
		return
	case n == "MissingIndexGroup":
		st.Missing = append(st.Missing, MissingIndex{Impact: num(attr(t, "Impact"))})
		ss.missing, ss.misDepth = len(st.Missing)-1, d
		return
	case ss.missing >= 0 && n == "MissingIndex":
		st.Missing[ss.missing].Obj = objRef(t)
		return
	case ss.missing >= 0 && n == "ColumnGroup":
		ss.usage = attr(t, "Usage")
		return
	case ss.missing >= 0 && n == "Column":
		m := &st.Missing[ss.missing]
		c := unquote(attr(t, "Name"))
		switch ss.usage {
		case "EQUALITY":
			m.Equality = append(m.Equality, c)
		case "INEQUALITY":
			m.Inequality = append(m.Inequality, c)
		case "INCLUDE":
			m.Include = append(m.Include, c)
		}
		return
	case n == "RelOp":
		p.startRelOp(ss, st, t, d)
		return
	}

	r := ss.rel()
	if r == nil {
		return
	}
	if d == r.depth+1 {
		switch n {
		case "OutputList":
			r.outDepth = d
		case "Warnings", "MemoryFractions", "RunTimeInformation", "RunTimePartitionSummary", "InternalInfo":
		default:
			if r.opName == "" {
				p.startOperator(ss, st, r, t, d)
			}
		}
		return
	}
	if r.outDepth > 0 && d == r.outDepth+1 && n == "ColumnReference" {
		r.output = append(r.output, colRef(t))
		return
	}
	if r.opDepth == 0 {
		return
	}
	if d == r.opDepth+1 {
		switch n {
		case "Object":
			o := objRef(t)
			if r.node.Obj == nil {
				r.node.Obj = &o
				if r.access >= 0 {
					st.Accesses[r.access].Obj = o
				}
			}
			if r.write >= 0 {
				st.Writes[r.write].Objs = append(st.Writes[r.write].Objs, o)
			}
		case "IndexedViewInfo":
			r.ivDepth = d
		case "SeekPredicates":
			r.seekDepth = d
		case "Predicate":
			r.predDepth = d
		case "SetPredicate":
			r.setDepth = d
		}
		switch columnRole(r.opName, r.node.LogicalOp, n) {
		case roleJoin:
			r.joinDepth = d
		case roleGroup:
			r.grpDepth = d
		}
		return
	}
	if r.ivDepth > 0 && d == r.ivDepth+1 && n == "Object" && r.access >= 0 {
		a := &st.Accesses[r.access]
		a.ViewBase = append(a.ViewBase, objRef(t))
		return
	}
	if r.seekDepth > 0 && r.access >= 0 {
		switch n {
		case "Prefix":
			r.seekEq = attr(t, "ScanType") == "EQ"
		case "StartRange", "EndRange":
			r.seekEq = false
		case "ColumnReference":
			if parent == "RangeColumns" {
				addSeek(&st.Accesses[r.access], colRef(t), r.seekEq)
			}
		}
		return
	}
	if r.predDepth > 0 && r.access >= 0 {
		p.predicateStart(ss, t, n, parent, d)
		return
	}
	if (r.joinDepth > 0 || r.grpDepth > 0) && n == "ColumnReference" {
		if c := colRef(t); c.Table != "" {
			if r.joinDepth > 0 {
				addCol(&st.JoinCols, c)
			} else {
				addCol(&st.GroupCols, c)
			}
		}
		return
	}
	if r.setDepth > 0 && r.write >= 0 && n == "ColumnReference" && parent == "Assign" && childIdx == 0 {
		// A wide Index Update assigns the index row's own columns (id1020),
		// which name no table: only the columns of the table are kept
		if c := colRef(t); c.Table != "" {
			w := &st.Writes[r.write]
			w.SetCols = append(w.SetCols, c)
		}
		return
	}
	if r.sortIdx >= 0 && parent == "OrderBy" && n == "OrderByColumn" && d == r.opDepth+2 {
		a := attr(t, "Ascending")
		s := &st.Sorts[r.sortIdx]
		s.Desc = append(s.Desc, a == "0" || a == "false")
		return
	}
	if r.sortIdx >= 0 && parent == "OrderByColumn" && n == "ColumnReference" && d == r.opDepth+3 {
		s := &st.Sorts[r.sortIdx]
		s.Cols = append(s.Cols, colRef(t))
	}
}

const (
	roleNone = iota
	roleJoin
	roleGroup
)

// columnRole says whether child, a direct child of the operator element op
// of a RelOp of logical operator logical, lists join columns, grouping
// columns or neither. A Hash Match or a Merge Join also serves for unions and
// distinct: only the join and aggregate forms count.
func columnRole(op, logical, child string) int {
	join := strings.Contains(logical, "Join")
	switch op {
	case "Hash":
		switch {
		case join && (child == "HashKeysBuild" || child == "HashKeysProbe" || child == "ProbeResidual"):
			return roleJoin
		case (strings.Contains(logical, "Aggregate") || logical == "Flow Distinct") &&
			(child == "HashKeysBuild" || child == "GroupBy"):
			return roleGroup
		}
	case "Merge":
		if join && (child == "InnerSideJoinColumns" || child == "OuterSideJoinColumns" || child == "Residual") {
			return roleJoin
		}
	case "NestedLoops":
		if child == "OuterReferences" || child == "Predicate" {
			return roleJoin
		}
	case "AdaptiveJoin":
		switch child {
		case "HashKeysBuild", "HashKeysProbe", "OuterReferences", "Predicate":
			return roleJoin
		}
	case "StreamAggregate":
		if child == "GroupBy" {
			return roleGroup
		}
	}
	return roleNone
}

func addCol(cols *[]ColRef, c ColRef) {
	if !slices.Contains(*cols, c) {
		*cols = append(*cols, c)
	}
}

func addSeek(a *Access, c ColRef, eq bool) {
	if c.Table == "" { // a bookmark or computed column, not a column of the table
		return
	}
	for i, s := range a.SeekCols {
		if s == c {
			a.SeekEq[i] = a.SeekEq[i] && eq
			return
		}
	}
	a.SeekCols = append(a.SeekCols, c)
	a.SeekEq = append(a.SeekEq, eq)
}

func (p *parser) startRelOp(ss *stmtState, st *Statement, t xml.StartElement, d int) {
	node := &RelOp{
		PhysicalOp:   attr(t, "PhysicalOp"),
		LogicalOp:    attr(t, "LogicalOp"),
		EstimateRows: num(attr(t, "EstimateRows")),
		SubtreeCost:  num(attr(t, "EstimatedTotalSubtreeCost")),
	}
	if par := attr(t, "Parallel"); par == "1" || par == "true" {
		st.Parallel = true
	}
	if r := ss.rel(); r != nil {
		r.node.Children = append(r.node.Children, node)
	} else if st.Root == nil {
		st.Root = node
	}
	r := &relState{node: node, depth: d, access: -1, write: -1, sortIdx: -1}
	if isWrite(node.PhysicalOp) {
		st.Writes = append(st.Writes, Write{Op: node.PhysicalOp})
		r.write = len(st.Writes) - 1
	}
	ss.rels = append(ss.rels, r)
	ss.all = append(ss.all, r)
}

func (p *parser) startOperator(ss *stmtState, st *Statement, r *relState, t xml.StartElement, d int) {
	r.opDepth, r.opName = d, t.Name.Local
	switch r.opName {
	case "IndexScan", "TableScan":
		st.Accesses = append(st.Accesses, Access{Op: r.node.PhysicalOp, LookupOf: -1, EstimateRows: r.node.EstimateRows})
		r.access = len(st.Accesses) - 1
		ss.accessOf[r.node] = r.access
		if attr(t, "Lookup") == "1" || attr(t, "Lookup") == "true" {
			ss.lookupOps[r.node] = true
		}
	case "Sort", "TopSort":
		st.Sorts = append(st.Sorts, SortReq{})
		r.sortIdx = len(st.Sorts) - 1
	}
}

// predicateStart follows the Compare elements of an access's residual
// predicate: each direct ScalarOperator child is an operand, classified by
// its first child (Identifier of a column or a variable, Const, or other).
func (p *parser) predicateStart(ss *stmtState, t xml.StartElement, n, parent string, d int) {
	if n == "Compare" {
		ss.compares = append(ss.compares, &compare{depth: d, op: attr(t, "CompareOp")})
		return
	}
	if len(ss.compares) == 0 {
		return
	}
	c := ss.compares[len(ss.compares)-1]
	if n == "ScalarOperator" && d == c.depth+1 {
		c.operands = append(c.operands, &operand{depth: d})
		return
	}
	if len(c.operands) == 0 {
		return
	}
	o := c.operands[len(c.operands)-1]
	switch {
	case d == o.depth+1 && !o.seen:
		o.seen = true
		o.kind = opOther // an Identifier stays so until its ColumnReference says otherwise
		if n == "Const" {
			o.kind = opValue
		}
	case d == o.depth+2 && n == "ColumnReference" && parent == "Identifier":
		col := colRef(t)
		switch {
		case strings.HasPrefix(col.Column, "@"):
			o.kind = opValue
		case col.Table != "":
			o.kind, o.col = opColumn, col
		}
	}
}

func (p *parser) end() {
	d := len(p.stack) - 1
	n := p.stack[d].name
	p.stack = p.stack[:d]
	if len(p.stmts) == 0 {
		return
	}
	ss := p.stmts[len(p.stmts)-1]
	if d == ss.depth {
		p.finishStatement(ss)
		p.stmts = p.stmts[:len(p.stmts)-1]
		return
	}
	st := p.stmt()
	switch {
	case d == ss.plDepth:
		ss.plDepth = 0
	case d == ss.qpDepth:
		ss.qpDepth = 0
	case ss.missing >= 0 && d == ss.misDepth:
		ss.missing = -1
	}
	r := ss.rel()
	if r == nil {
		return
	}
	if n == "RelOp" && d == r.depth {
		if r.access >= 0 {
			st.Accesses[r.access].Output = r.output
		}
		if r.write >= 0 {
			own := r.node.SubtreeCost
			for _, c := range r.node.Children {
				own -= c.SubtreeCost
			}
			st.Writes[r.write].OwnCost = own
		}
		ss.rels = ss.rels[:len(ss.rels)-1]
		return
	}
	switch d {
	case r.outDepth:
		r.outDepth = 0
	case r.opDepth:
		r.opDepth = 0
	case r.ivDepth:
		r.ivDepth = 0
	case r.seekDepth:
		r.seekDepth = 0
	case r.predDepth:
		r.predDepth = 0
	case r.setDepth:
		r.setDepth = 0
	}
	// Apart from the switch: a Predicate of a Nested Loops sets predDepth
	// and joinDepth to the same depth.
	if d == r.joinDepth {
		r.joinDepth = 0
	}
	if d == r.grpDepth {
		r.grpDepth = 0
	}
	if n == "Compare" && len(ss.compares) > 0 && ss.compares[len(ss.compares)-1].depth == d {
		c := ss.compares[len(ss.compares)-1]
		ss.compares = ss.compares[:len(ss.compares)-1]
		if r.access >= 0 {
			addPredicate(&st.Accesses[r.access], c)
		}
	}
}

func addPredicate(a *Access, c *compare) {
	if len(c.operands) != 2 {
		return
	}
	x, y := c.operands[0], c.operands[1]
	if x.kind != opColumn {
		x, y = y, x
	}
	if x.kind != opColumn || y.kind != opValue {
		return
	}
	switch c.op {
	case "EQ":
		a.PredEq = append(a.PredEq, x.col)
	case "LT", "LE", "GT", "GE":
		a.PredRange = append(a.PredRange, x.col)
	}
}

// finishStatement links each lookup to the read it completes: the lookup and
// that read are siblings under a Nested Loops.
func (p *parser) finishStatement(ss *stmtState) {
	st := &p.plan.Statements[ss.idx]
	for _, r := range ss.all {
		for _, c := range r.node.Children {
			if !ss.lookupOps[c] {
				continue
			}
			for _, sib := range r.node.Children {
				if sib == c {
					continue
				}
				if i := firstAccess(ss, sib); i >= 0 {
					st.Accesses[ss.accessOf[c]].LookupOf = i
					break
				}
			}
		}
	}
}

func firstAccess(ss *stmtState, n *RelOp) int {
	if i, ok := ss.accessOf[n]; ok {
		return i
	}
	for _, c := range n.Children {
		if i := firstAccess(ss, c); i >= 0 {
			return i
		}
	}
	return -1
}
