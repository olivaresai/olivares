// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxDiffFileBytes bounds each side of one file's text diff; MaxDiffTotalBytes
	// bounds the diff text of a whole comparison. A file past either keeps its
	// hashes and sizes and names why it has no text.
	MaxDiffFileBytes  = 64 << 10
	MaxDiffTotalBytes = 512 << 10
	// maxDiffCells bounds the line-comparison table after the common head and tail
	// of a file are cut.
	maxDiffCells = 4_000_000
	diffContext  = 3
)

// FileChange is one path that differs between two revisions. A hash, size or
// mode that did not change is still reported when the other side did.
type FileChange struct {
	Path        string `json:"path"`
	Change      string `json:"change"`
	OldSHA256   string `json:"old_sha256,omitempty"`
	NewSHA256   string `json:"new_sha256,omitempty"`
	OldSize     int64  `json:"old_size,omitempty"`
	NewSize     int64  `json:"new_size,omitempty"`
	OldMode     uint32 `json:"old_mode,omitempty"`
	NewMode     uint32 `json:"new_mode,omitempty"`
	Diff        string `json:"diff,omitempty"`
	DiffOmitted string `json:"diff_omitted,omitempty"`
}

// MemberChange is one skill an update adds, removes or changes. Scripts and
// ActiveContent describe the new side; NewActiveContent is what the old side
// did not already carry, which is what a reviewer must look at first.
type MemberChange struct {
	Kind             string   `json:"kind,omitempty"`
	Name             string   `json:"name"`
	Change           string   `json:"change"`
	Description      string   `json:"description,omitempty"`
	Scripts          []string `json:"scripts,omitempty"`
	ActiveContent    []string `json:"active_content,omitempty"`
	NewActiveContent []string `json:"new_active_content,omitempty"`
}

// PackDiff is the reviewable difference between two packs.
type PackDiff struct {
	Members []MemberChange `json:"members"`
	Files   []FileChange   `json:"files"`
}

// DiffPacks compares two validated packs. A nil from lists everything as added.
// Both sides are the importer's verified snapshots, so the diff shows the bytes
// a session would receive, never a source's own claim.
func DiffPacks(from, to *ValidatedPack) PackDiff {
	out := PackDiff{Members: []MemberChange{}, Files: []FileChange{}}
	oldManifest, newManifest := map[string]FileEntry{}, map[string]FileEntry{}
	oldMembers, newMembers := map[string]Member{}, map[string]Member{}
	if from != nil {
		for _, e := range from.Manifest {
			oldManifest[e.Path] = e
		}
		for _, m := range from.Members {
			oldMembers[m.Name] = m
		}
	}
	if to != nil {
		for _, e := range to.Manifest {
			newManifest[e.Path] = e
		}
		for _, m := range to.Members {
			newMembers[m.Name] = m
		}
	}
	names := map[string]bool{}
	for name := range oldMembers {
		names[name] = true
	}
	for name := range newMembers {
		names[name] = true
	}
	for _, name := range sortedKeys(names) {
		prev, hadPrev := oldMembers[name]
		next, hasNext := newMembers[name]
		switch {
		case hadPrev && !hasNext:
			out.Members = append(out.Members, MemberChange{Kind: prev.Kind, Name: name, Change: "removed"})
		case !hadPrev || prev.ContentDigest != next.ContentDigest:
			change := MemberChange{Kind: next.Kind, Name: name, Change: "changed", Description: next.Description, Scripts: next.Scripts, ActiveContent: next.ActiveContent()}
			if !hadPrev {
				change.Change = "added"
			}
			before := prev.ActiveContent()
			for _, item := range change.ActiveContent {
				if !slices.Contains(before, item) {
					change.NewActiveContent = append(change.NewActiveContent, item)
				}
			}
			out.Members = append(out.Members, change)
		}
	}
	paths := map[string]bool{}
	for p := range oldManifest {
		paths[p] = true
	}
	for p := range newManifest {
		paths[p] = true
	}
	budget := MaxDiffTotalBytes
	// A skill's own instructions come first, so a large support file cannot use the
	// budget up before the text a reviewer most needs.
	ordered := sortedKeys(paths)
	sort.SliceStable(ordered, func(i, j int) bool {
		return strings.HasSuffix(ordered[i], "/SKILL.md") && !strings.HasSuffix(ordered[j], "/SKILL.md")
	})
	for _, p := range ordered {
		prev, hadPrev := oldManifest[p]
		next, hasNext := newManifest[p]
		if hadPrev && hasNext && prev.Digest == next.Digest && prev.Mode == next.Mode {
			continue
		}
		change := FileChange{Path: p, OldSHA256: prev.Digest, NewSHA256: next.Digest, OldSize: prev.Size, NewSize: next.Size, OldMode: prev.Mode, NewMode: next.Mode}
		switch {
		case !hadPrev:
			change.Change = "added"
		case !hasNext:
			change.Change = "removed"
		default:
			change.Change = "changed"
		}
		if hadPrev && hasNext && prev.Digest == next.Digest {
			out.Files = append(out.Files, change) // mode only: no text to show
			continue
		}
		var oldBody, newBody []byte
		if hadPrev {
			oldBody, _ = from.File(p)
		}
		if hasNext {
			newBody, _ = to.File(p)
		}
		change.Diff, change.DiffOmitted = textDiff(p, oldBody, newBody, hadPrev, hasNext, &budget)
		out.Files = append(out.Files, change)
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// textDiff renders a unified diff of one file, or says why it cannot: binary
// bytes, a side past MaxDiffFileBytes, a table past maxDiffCells, or a
// comparison whose diff text already filled its budget.
func textDiff(path string, oldBody, newBody []byte, hadOld, hasNew bool, budget *int) (string, string) {
	if *budget <= 0 {
		return "", "limit"
	}
	if len(oldBody) > MaxDiffFileBytes || len(newBody) > MaxDiffFileBytes {
		return "", "too_large"
	}
	for _, body := range [][]byte{oldBody, newBody} {
		if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
			return "", "binary"
		}
	}
	ops, ok := lineOps(splitLines(oldBody), splitLines(newBody))
	if !ok {
		return "", "too_large"
	}
	oldName, newName := "a/"+path, "b/"+path
	if !hadOld {
		oldName = "/dev/null"
	}
	if !hasNew {
		newName = "/dev/null"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", oldName, newName)
	writeHunks(&sb, ops)
	if sb.Len() > *budget {
		*budget = 0
		return "", "limit"
	}
	*budget -= sb.Len()
	return sb.String(), ""
}

// UnifiedTextDiff is the unified diff of one file between two texts, for a caller that
// compares something other than two packs. hadOld and hasNew say which sides exist
// (an absent side is /dev/null). The omitted reason is "binary", "too_large" or
// "limit" when there is no text; budget is the bytes of diff text the caller still
// accepts, spent as diffs are returned (start it at MaxDiffTotalBytes).
func UnifiedTextDiff(path string, oldBody, newBody []byte, hadOld, hasNew bool, budget *int) (diff, omitted string) {
	return textDiff(path, oldBody, newBody, hadOld, hasNew, budget)
}

func splitLines(body []byte) []string {
	lines := strings.SplitAfter(string(body), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type lineOp struct {
	kind byte // ' ', '-' or '+'
	text string
}

// lineOps is the edit script from a to b: the common head and tail are cut
// first, which is nearly every real revision, then a longest-common-subsequence
// table orders what is left. false means the table would pass maxDiffCells.
func lineOps(a, b []string) ([]lineOp, bool) {
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	am, bm := a[head:len(a)-tail], b[head:len(b)-tail]
	n, m := len(am), len(bm)
	if (n+1)*(m+1) > maxDiffCells {
		return nil, false
	}
	table := make([]uint32, (n+1)*(m+1))
	at := func(i, j int) *uint32 { return &table[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if am[i] == bm[j] {
				*at(i, j) = *at(i+1, j+1) + 1
			} else {
				*at(i, j) = max(*at(i+1, j), *at(i, j+1))
			}
		}
	}
	ops := make([]lineOp, 0, head+n+m+tail)
	for _, line := range a[:head] {
		ops = append(ops, lineOp{' ', line})
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && am[i] == bm[j]:
			ops = append(ops, lineOp{' ', am[i]})
			i, j = i+1, j+1
		case i < n && (j == m || *at(i+1, j) >= *at(i, j+1)):
			ops = append(ops, lineOp{'-', am[i]})
			i++
		default:
			ops = append(ops, lineOp{'+', bm[j]})
			j++
		}
	}
	for _, line := range a[len(a)-tail:] {
		ops = append(ops, lineOp{' ', line})
	}
	return ops, true
}

// writeHunks groups the changed lines with diffContext lines around them;
// changes closer than twice that share a hunk.
func writeHunks(sb *strings.Builder, ops []lineOp) {
	var changed []int
	for i, op := range ops {
		if op.kind != ' ' {
			changed = append(changed, i)
		}
	}
	for k := 0; k < len(changed); {
		start, end := max(changed[k]-diffContext, 0), min(changed[k]+diffContext+1, len(ops))
		k++
		for k < len(changed) && changed[k]-diffContext <= end {
			end = min(changed[k]+diffContext+1, len(ops))
			k++
		}
		oldStart, newStart := 1, 1
		for _, op := range ops[:start] {
			if op.kind != '+' {
				oldStart++
			}
			if op.kind != '-' {
				newStart++
			}
		}
		oldCount, newCount := 0, 0
		for _, op := range ops[start:end] {
			if op.kind != '+' {
				oldCount++
			}
			if op.kind != '-' {
				newCount++
			}
		}
		// An empty range names the line before it, as diff(1) does.
		if oldCount == 0 {
			oldStart--
		}
		if newCount == 0 {
			newStart--
		}
		fmt.Fprintf(sb, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, op := range ops[start:end] {
			sb.WriteByte(op.kind)
			sb.WriteString(op.text)
			if !strings.HasSuffix(op.text, "\n") {
				sb.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
}
