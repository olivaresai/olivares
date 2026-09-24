// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"strconv"
	"strings"
)

// generate returns the Cedar text of one cell at one point. The same text is
// produced independently by the matrix builder; the child compares digests
// before any setup, so a generator defect is a mismatch, never a cost.
func generate(cell string, point int, requestTime int64) (string, error) {
	if point < 1 {
		return "", fmt.Errorf("point %d is not positive", point)
	}
	switch cell {
	case "R1a": // one entity path of `point` segments
		return "permit(principal == " + strings.Repeat("A::", point) + `"x", action, resource);`, nil
	case "R1b": // `point` distinct bare annotations
		var b strings.Builder
		for i := 0; i < point; i++ {
			b.WriteString("@")
			b.WriteString(annotationKey(i))
			b.WriteString(" ")
		}
		b.WriteString("permit(principal, action, resource);")
		return b.String(), nil
	case "R2a": // parentheses of depth `point`
		return when(strings.Repeat("(", point) + "true" + strings.Repeat(")", point)), nil
	case "R2b": // nested if of depth `point`
		return when(strings.Repeat("if true then ", point) + "true" + strings.Repeat(" else false", point)), nil
	case "R3a": // a literal like with a long final chunk
		return when(likeLiterals(point, false)), nil
	case "R3b": // the R3a literal shared by `point` has references, n = 4,096
		return when("(" + likeLiterals(4096, false) + ") has " + attributePath(point)), nil
	case "R3c": // `point` pairs of equal sum, folded
		return when(pairSet(point, false) + " == []"), nil
	case "R3d": // R3c plus one element that does not fold
		return when(pairSet(point, true) + " == []"), nil
	case "R3e": // a literal like with a long non-final chunk
		return when(likeLiterals(point, true)), nil
	case "R4a", "W":
		return when("context has " + attributePath(point)), nil
	case "R4b":
		return when("{} has " + attributePath(point)), nil
	case "R4c": // a nested record literal with a non-foldable leaf
		return when(nestedRecord(point, "context.time") + " has " + attributePath(point)), nil
	case "R4d": // a dynamic if around a constant nested record
		return when("(if context.time > 0 then " + nestedRecord(point, "1") + " else {}) has " + attributePath(point)), nil
	case "R5a": // a chain of `point` negations
		return when(strings.Repeat("!", point) + "context.a"), nil
	case "R5b": // `point` conditions
		return "permit(principal, action, resource)" + strings.Repeat(" when{true}", point) + ";", nil
	case "R5c": // folded nested-set equality
		return when(nestedSet(point, "1") + " == " + nestedSet(point, "1")), nil
	case "R5d": // nested-set equality with a non-foldable side
		return when(nestedSet(point, "context.time") + " == " + nestedSet(point, strconv.FormatInt(requestTime, 10))), nil
	}
	return "", fmt.Errorf("unknown cell %q", cell)
}

func when(body string) string {
	return "permit(principal, action, resource) when { " + body + " };"
}

// attributePath returns a1.a2...ak.
func attributePath(k int) string {
	var b strings.Builder
	for i := 1; i <= k; i++ {
		if i > 1 {
			b.WriteString(".")
		}
		b.WriteString("a")
		b.WriteString(strconv.Itoa(i))
	}
	return b.String()
}

// nestedRecord returns {a1:{a2:...{ak:leaf}...}}.
func nestedRecord(k int, leaf string) string {
	var b strings.Builder
	for i := 1; i <= k; i++ {
		b.WriteString("{a")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(":")
	}
	b.WriteString(leaf)
	b.WriteString(strings.Repeat("}", k))
	return b.String()
}

func nestedSet(d int, leaf string) string {
	return strings.Repeat("[", d) + leaf + strings.Repeat("]", d)
}

// likeLiterals returns "a...a" like "*a...ab", n bytes each side, with an
// optional trailing wildcard that makes the long chunk non-final.
func likeLiterals(n int, trailingWildcard bool) string {
	pattern := "*" + strings.Repeat("a", n-1) + "b"
	if trailingWildcard {
		pattern += "*"
	}
	return `"` + strings.Repeat("a", n) + `" like "` + pattern + `"`
}

// pairSet returns [[1,32767],[2,32766],...] with n pairs of equal sum, and an
// optional final [context.time].
func pairSet(n int, dynamic bool) string {
	const sum = 32768
	var b strings.Builder
	b.WriteString("[")
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		b.WriteString("[")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(",")
		b.WriteString(strconv.Itoa(sum - i))
		b.WriteString("]")
	}
	if dynamic {
		b.WriteString(",[context.time]")
	}
	b.WriteString("]")
	return b.String()
}

// annotationKey returns the i-th three-letter key: aaa, aab, ..., ZZZ.
func annotationKey(i int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	n := len(alphabet)
	return string([]byte{alphabet[(i/(n*n))%n], alphabet[(i/n)%n], alphabet[i%n]})
}
