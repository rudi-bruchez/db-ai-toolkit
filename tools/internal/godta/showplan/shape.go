package showplan

import (
	"math"
	"strings"
)

// Shape is the operator tree of s in prefix order, each node written as its
// PhysicalOp followed by Schema.Table.Index in parentheses when it has an
// object, nodes separated by |. The database is left out, so a plan of the
// source and one of a lab clone with another name compare equal. Two plans
// have the same shape if and only if their Shape strings are equal.
func Shape(s Statement) string {
	var parts []string
	var walk func(*RelOp)
	walk = func(n *RelOp) {
		part := n.PhysicalOp
		if n.Obj != nil {
			part += "(" + n.Obj.Schema + "." + n.Obj.Table + "." + n.Obj.Index + ")"
		}
		parts = append(parts, part)
		for _, c := range n.Children {
			walk(c)
		}
	}
	if s.Root != nil {
		walk(s.Root)
	}
	return strings.Join(parts, "|")
}

// SameCost reports whether two costs differ by a relative gap below 1e-6.
func SameCost(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) < 1e-6*math.Max(math.Abs(a), math.Abs(b))
}
