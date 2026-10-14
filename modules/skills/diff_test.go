// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/skills"
)

func skillText(name, extraFrontmatter, body string) string {
	return "---\nname: " + name + "\ndescription: Skill " + name + ".\nlicense: MIT\n" + extraFrontmatter + "---\n" + body
}

func packOf(t *testing.T, names, contents []string) *skills.ValidatedPack {
	t.Helper()
	pack, err := skills.ImportArchive(context.Background(), bytes.NewReader(archive(t, names, contents)), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func changeOf(t *testing.T, d skills.PackDiff, path string) skills.FileChange {
	t.Helper()
	for _, f := range d.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no change for %s in %+v", path, d.Files)
	return skills.FileChange{}
}

func TestDiffPacksReportsMembersFilesAndALineDiff(t *testing.T) {
	old := packOf(t,
		[]string{"alpha/SKILL.md", "alpha/notes.md", "beta/SKILL.md", "gamma/SKILL.md"},
		[]string{skillText("alpha", "", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"), "same notes\n", skillText("beta", "", "beta body\n"), skillText("gamma", "", "gamma body\n")})
	next := packOf(t,
		[]string{"alpha/SKILL.md", "alpha/notes.md", "alpha/more.md", "beta/SKILL.md", "delta/SKILL.md"},
		[]string{skillText("alpha", "", "one\ntwo\nthree\nfour\nFIVE\nsix\nseven\neight\nnine\nten\n"), "same notes\n", "extra\n", skillText("beta", "", "beta body\n"), skillText("delta", "", "delta body\n")})

	d := skills.DiffPacks(old, next)

	members := map[string]string{}
	for _, m := range d.Members {
		members[m.Name] = m.Change
	}
	if want := map[string]string{"alpha": "changed", "delta": "added", "gamma": "removed"}; !reflect.DeepEqual(members, want) {
		t.Fatalf("members = %v, want %v", members, want)
	}
	if got := changeOf(t, d, "alpha/more.md").Change; got != "added" {
		t.Fatalf("alpha/more.md = %q", got)
	}
	if got := changeOf(t, d, "gamma/SKILL.md").Change; got != "removed" {
		t.Fatalf("gamma/SKILL.md = %q", got)
	}
	for _, f := range d.Files {
		if f.Path == "alpha/notes.md" || f.Path == "beta/SKILL.md" {
			t.Fatalf("unchanged file listed: %+v", f)
		}
	}
	skill := changeOf(t, d, "alpha/SKILL.md")
	if skill.Change != "changed" || skill.OldSHA256 == "" || skill.NewSHA256 == "" || skill.OldSHA256 == skill.NewSHA256 {
		t.Fatalf("changed file: %+v", skill)
	}
	for _, want := range []string{"@@ ", "-five\n", "+FIVE\n", " four\n", " six\n"} {
		if !strings.Contains(skill.Diff, want) {
			t.Fatalf("unified diff lacks %q:\n%s", want, skill.Diff)
		}
	}
	if strings.Contains(skill.Diff, " one\n") {
		t.Fatalf("diff carries more than three lines of context:\n%s", skill.Diff)
	}
	added := changeOf(t, d, "alpha/more.md")
	if !strings.Contains(added.Diff, "+extra\n") {
		t.Fatalf("added file diff: %q", added.Diff)
	}
}

func TestDiffPacksOfIdenticalPacksIsEmptyAndNilFromListsEverythingAdded(t *testing.T) {
	a := packOf(t, []string{"alpha/SKILL.md"}, []string{skillText("alpha", "", "body\n")})
	b := packOf(t, []string{"alpha/SKILL.md"}, []string{skillText("alpha", "", "body\n")})
	if d := skills.DiffPacks(a, b); len(d.Members) != 0 || len(d.Files) != 0 {
		t.Fatalf("identical packs differ: %+v", d)
	}
	d := skills.DiffPacks(nil, a)
	if len(d.Members) != 1 || d.Members[0].Change != "added" || len(d.Files) != 1 || d.Files[0].Change != "added" {
		t.Fatalf("first revision: %+v", d)
	}
}

func TestDiffPacksNamesWhatARevisionNewlyCanRun(t *testing.T) {
	old := packOf(t,
		[]string{"alpha/SKILL.md", "alpha/scripts/run.sh"},
		[]string{skillText("alpha", "", "body\n"), "#!/bin/sh\necho one\n"})
	next := packOf(t,
		[]string{"alpha/SKILL.md", "alpha/scripts/run.sh", "alpha/scripts/new.sh", "beta/SKILL.md", "beta/hooks.json"},
		[]string{skillText("alpha", "allowed-tools: Bash\nhooks:\n  PreToolUse: []\n", "run it !`date`\n"), "#!/bin/sh\necho one\n", "#!/bin/sh\necho two\n", skillText("beta", "", "beta\n"), "{}\n"})

	d := skills.DiffPacks(old, next)
	byName := map[string]skills.MemberChange{}
	for _, m := range d.Members {
		byName[m.Name] = m
	}
	alpha := byName["alpha"]
	for _, want := range []string{"allowed-tools", "hooks", "dynamic-command-interpolation"} {
		if !contains(alpha.NewActiveContent, want) {
			t.Fatalf("alpha new active content %v lacks %s", alpha.NewActiveContent, want)
		}
	}
	if !contains(alpha.Scripts, "scripts/new.sh") {
		t.Fatalf("alpha scripts %v lack the new script", alpha.Scripts)
	}
	if !contains(byName["beta"].ActiveContent, "hooks.json") {
		t.Fatalf("beta active content %v lacks its hooks file", byName["beta"].ActiveContent)
	}
	// A frontmatter key that grants nothing is shown as an extension, not as something that runs.
	harmlessKeys := packOf(t, []string{"alpha/SKILL.md"}, []string{skillText("alpha", "origin: ECC\ntools: Read\n", "body\n")})
	if got := skills.DiffPacks(nil, harmlessKeys).Members[0].ActiveContent; len(got) != 0 {
		t.Fatalf("a descriptive key is active content: %v", got)
	}
}

func TestDiffPacksReportsBinaryAndOversizeFilesWithoutTextDiff(t *testing.T) {
	big := strings.Repeat("a line of text\n", 6000) // 90 KB, over the per-file diff bound
	old := packOf(t,
		[]string{"alpha/SKILL.md", "alpha/blob.bin", "alpha/big.md"},
		[]string{skillText("alpha", "", "body\n"), "\x00\x01\x02", big})
	next := packOf(t,
		[]string{"alpha/SKILL.md", "alpha/blob.bin", "alpha/big.md"},
		[]string{skillText("alpha", "", "body\n"), "\x00\x01\x03", big + "tail\n"})

	d := skills.DiffPacks(old, next)
	if f := changeOf(t, d, "alpha/blob.bin"); f.Change != "changed" || f.Diff != "" || f.DiffOmitted != "binary" {
		t.Fatalf("binary: %+v", f)
	}
	if f := changeOf(t, d, "alpha/big.md"); f.Change != "changed" || f.Diff != "" || f.DiffOmitted != "too_large" {
		t.Fatalf("oversize: %+v", f)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestDiffPacksReportsAnExecutableBitFlipAsAChange(t *testing.T) {
	zipOf := func(mode uint32) *skills.ValidatedPack {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		for name, body := range map[string]string{"alpha/SKILL.md": skillText("alpha", "", "body\n"), "alpha/tool.sh": "#!/bin/sh\n"} {
			h := &zip.FileHeader{Name: name, Method: zip.Deflate}
			h.SetMode(0o644)
			if name == "alpha/tool.sh" {
				h.SetMode(fs.FileMode(mode))
			}
			f, err := w.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		pack, err := skills.ImportArchive(context.Background(), bytes.NewReader(b.Bytes()), "zip", "")
		if err != nil {
			t.Fatal(err)
		}
		return pack
	}
	d := skills.DiffPacks(zipOf(0o644), zipOf(0o755))
	if f := changeOf(t, d, "alpha/tool.sh"); f.Change != "changed" || f.OldSHA256 != f.NewSHA256 || f.OldMode == f.NewMode || f.Diff != "" {
		t.Fatalf("mode flip: %+v", f)
	}
	if len(d.Files) != 1 {
		t.Fatalf("only the flipped file differs: %+v", d.Files)
	}
}

func TestDiffPacksGroupsDistantEditsIntoHunksWithExactHeadersAndTheNoNewlineMarker(t *testing.T) {
	var old, next []string
	for i := 1; i <= 20; i++ {
		line := "l" + strconv.Itoa(i)
		old = append(old, line)
		if i == 18 {
			continue // deleted
		}
		next = append(next, line)
		if i == 2 {
			next = append(next, "NEW") // inserted
		}
	}
	// Both files end without a newline.
	before := packOf(t, []string{"alpha/SKILL.md", "alpha/notes.md"}, []string{skillText("alpha", "", "b\n"), strings.Join(old, "\n")})
	after := packOf(t, []string{"alpha/SKILL.md", "alpha/notes.md"}, []string{skillText("alpha", "", "b\n"), strings.Join(next, "\n")})

	diff := changeOf(t, skills.DiffPacks(before, after), "alpha/notes.md").Diff
	for _, want := range []string{"@@ -1,5 +1,6 @@\n", "@@ -15,6 +16,5 @@\n", "+NEW\n", "-l18\n", " l20\n\\ No newline at end of file\n"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff lacks %q:\n%s", want, diff)
		}
	}
	if got := strings.Count(diff, "@@ -"); got != 2 {
		t.Fatalf("two distant edits made %d hunks:\n%s", got, diff)
	}
}

// A comparison's diff text is bounded, and a skill's own instructions are never the
// ones that run out of room: SKILL.md is rendered before large support files.
func TestDiffPacksBudgetSpendsOnInstructionsFirstAndOmitsTheRest(t *testing.T) {
	rewritten := func(prefix string) string {
		var b strings.Builder
		for i := 0; i < 1000; i++ { // ~60 KB, below the per-file bound
			b.WriteString(prefix + strings.Repeat("x", 50) + strconv.Itoa(1000+i) + "\n")
		}
		return b.String()
	}
	names := []string{"zeta/SKILL.md"}
	oldFiles := []string{skillText("zeta", "", "old instructions\n")}
	newFiles := []string{skillText("zeta", "", "new instructions\n")}
	for i := 0; i < 8; i++ {
		// Upper-case names sort before SKILL.md, so only the ordering rule can keep the
		// instructions first.
		name := "zeta/AUX-" + strconv.Itoa(i) + ".txt"
		names = append(names, name)
		oldFiles = append(oldFiles, rewritten("a"))
		newFiles = append(newFiles, rewritten("b"))
	}
	d := skills.DiffPacks(packOf(t, names, oldFiles), packOf(t, names, newFiles))
	if skill := changeOf(t, d, "zeta/SKILL.md"); skill.Diff == "" || skill.DiffOmitted != "" {
		t.Fatalf("the instructions lost their diff to a support file: %+v", skill)
	}
	omitted := 0
	total := 0
	for _, f := range d.Files {
		total += len(f.Diff)
		if f.DiffOmitted == "limit" {
			omitted++
		}
	}
	if omitted == 0 || total > skills.MaxDiffTotalBytes {
		t.Fatalf("omitted %d files, %d diff bytes (bound %d)", omitted, total, skills.MaxDiffTotalBytes)
	}
}

func TestMemberActiveContentRecognizesWhatALoadRuns(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		active bool
	}{
		"inline command":          {"Today is !`date`.\n", true},
		"fenced command":          {"Run:\n```!\nls -la\n```\n", true},
		"indented fenced command": {"Run:\n  ```!\nls\n```\n", true},
		"fence in a list item":    {"- ```!\nls\n```\n", true},
		"fence in a quote":        {"> ```!\n> ls\n> ```\n", true},
		"fence after a colon":     {"Run: ```!\nls\n```\n", true},
		"command over two lines":  {"Output: !`ls\n-la` here.\n", true},
		"macro in prose":          {"Use `vec!` and `println!` for output.\n", false},
		"a bare bang in a span":   {"Excessive `!` (bang operator) instead of null checks.\n", false},
		"a code sample":           {"```rust\nassert!(x);\n```\n", false},
		"nothing":                 {"Plain instructions.\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			pack := packOf(t, []string{"alpha/SKILL.md"}, []string{skillText("alpha", "", tc.body)})
			if got := len(pack.Members[0].ActiveContent()) > 0; got != tc.active {
				t.Fatalf("active = %v, want %v (extensions %v)", got, tc.active, pack.Members[0].Extensions)
			}
		})
	}
	// A key carrying terminal control bytes is listed without them.
	evil := packOf(t, []string{"alpha/SKILL.md"}, []string{"---\nname: alpha\ndescription: d\n\"hooks\\u001b[31m/x\": 1\n---\nbody\n"})
	for _, item := range evil.Members[0].Extensions {
		if strings.ContainsAny(item, "\x1b") {
			t.Fatalf("a control byte survived into the extension list: %q", item)
		}
	}
}

func TestMemberActiveContentIsBoundedButNeverEmptiedByTheBound(t *testing.T) {
	var keys strings.Builder
	for i := 0; i < 40; i++ {
		keys.WriteString("\"dir" + strconv.Itoa(i) + "/key\": 1\n")
	}
	pack := packOf(t, []string{"alpha/SKILL.md"}, []string{"---\nname: alpha\ndescription: d\n" + keys.String() + "---\nbody\n"})
	if got := len(pack.Members[0].ActiveContent()); got == 0 || got > 16 {
		t.Fatalf("active items = %d, want 1..16", got)
	}
}
