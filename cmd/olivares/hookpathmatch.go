// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"path/filepath"
	"strings"
)

func normalizePath(ref, root string) (abs string, ok bool) {
	if ref == "" || strings.ContainsRune(ref, '\x00') || strings.HasPrefix(ref, "~") || strings.ContainsAny(ref, "*?[]{}") {
		return "", false
	}
	if filepath.IsAbs(ref) {
		abs = ref
	} else {
		if root == "" || !filepath.IsAbs(root) {
			return "", false
		}
		abs = filepath.Join(root, ref)
	}
	return filepath.Clean(abs), true
}

func pathGlobMatch(glob, abs string) bool {
	if glob == "" || abs == "" {
		return false
	}
	glob = filepath.Clean(glob)
	abs = filepath.Clean(abs)
	if !filepath.IsAbs(glob) || !filepath.IsAbs(abs) {
		return false
	}
	return matchPathSegments(pathSegments(glob), pathSegments(abs))
}

func pathInSubtree(abs, subtreeAbs string) bool {
	return abs == subtreeAbs || strings.HasPrefix(abs, subtreeAbs+"/")
}

func pathSegments(cleanAbs string) []string {
	trimmed := strings.Trim(cleanAbs, string(filepath.Separator))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, string(filepath.Separator))
}

func matchPathSegments(pattern, path []string) bool {
	type state struct {
		p int
		s int
	}
	seen := map[state]bool{}
	memo := map[state]bool{}
	var match func(int, int) bool
	match = func(pi, si int) bool {
		st := state{p: pi, s: si}
		if seen[st] {
			return memo[st]
		}
		seen[st] = true

		var ok bool
		switch {
		case pi == len(pattern):
			ok = si == len(path)
		case pattern[pi] == "**":
			for next := si; next <= len(path); next++ {
				if match(pi+1, next) {
					ok = true
					break
				}
			}
		case si < len(path) && segmentGlobMatch(pattern[pi], path[si]):
			ok = match(pi+1, si+1)
		}

		memo[st] = ok
		return ok
	}
	return match(0, 0)
}

func segmentGlobMatch(pattern, segment string) bool {
	pr := []rune(pattern)
	sr := []rune(segment)
	type state struct {
		p int
		s int
	}
	seen := map[state]bool{}
	memo := map[state]bool{}
	var match func(int, int) bool
	match = func(pi, si int) bool {
		st := state{p: pi, s: si}
		if seen[st] {
			return memo[st]
		}
		seen[st] = true

		var ok bool
		switch {
		case pi == len(pr):
			ok = si == len(sr)
		case pr[pi] == '*':
			for next := si; next <= len(sr); next++ {
				if match(pi+1, next) {
					ok = true
					break
				}
			}
		case si < len(sr) && pr[pi] == '?':
			ok = match(pi+1, si+1)
		case si < len(sr) && pr[pi] == sr[si]:
			ok = match(pi+1, si+1)
		}

		memo[st] = ok
		return ok
	}
	return match(0, 0)
}
