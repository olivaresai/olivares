// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
)

// walkResult is what the admission walk counts for one policy.
type walkResult struct {
	Steps int `json:"steps"`
	Depth int `json:"depth"`
}

// walkEntry is one pending body reference and its depth (a body root is 1).
type walkEntry struct {
	node  xast.IsNode
	depth int
}

// walkPolicy reproduces the admission walk: one step for the policy, each
// annotation, each scope node and each scope entity, and one step for every
// body reference each time it is reached. Children come only from cedar-go,
// through a one-level Inspect, and the pending references live on an explicit
// heap stack, so the Go stack depth does not follow the tree. The depth is
// s + c + b + 1: s scope constraints that are not "all", c conditions, and b
// the depth of the deepest body reference.
//
// This is the algorithm only. It is not the product's admission code.
func walkPolicy(p *xast.Policy) walkResult {
	steps := 1 + len(p.Annotations)
	constraints := 0
	for _, scope := range []xast.IsScopeNode{p.Principal, p.Action, p.Resource} {
		steps++
		switch s := scope.(type) {
		case xast.ScopeTypeAll:
		case xast.ScopeTypeEq, xast.ScopeTypeIn, xast.ScopeTypeIsIn:
			steps++
			constraints++
		case xast.ScopeTypeInSet:
			steps += len(s.Entities)
			constraints++
		case xast.ScopeTypeIs:
			constraints++
		}
	}
	deepest := 0
	var stack []walkEntry
	var children []xast.IsNode
	for i := len(p.Conditions) - 1; i >= 0; i-- {
		stack = append(stack, walkEntry{node: p.Conditions[i].Body, depth: 1})
	}
	for len(stack) > 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		steps++
		if e.depth > deepest {
			deepest = e.depth
		}
		children = directChildren(e.node, children)
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, walkEntry{node: children[i], depth: e.depth + 1})
		}
	}
	return walkResult{Steps: steps, Depth: constraints + len(p.Conditions) + deepest + 1}
}

// directChildren asks cedar-go for the direct children of n, in order. The
// callback accepts the root and refuses to descend into every child.
func directChildren(n xast.IsNode, buf []xast.IsNode) []xast.IsNode {
	buf = buf[:0]
	root := true
	xast.Inspect(xast.NewNode(n), func(c xast.IsNode) bool {
		if root {
			root = false
			return true
		}
		buf = append(buf, c)
		return false
	})
	return buf
}
