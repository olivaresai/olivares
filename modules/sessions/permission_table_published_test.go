// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The table a person reads in Settings and in the docs is the table the launch
// builders produce: both files are written from PermissionTable, and this test
// fails when either one differs from it. Regenerate them with
//
//	OLV_UPDATE_PERMISSION_TABLE=1 go test ./modules/sessions -run TestPermissionTable_PublishedFilesAreCurrent
const (
	permissionTableJSONPath = "../../web/src/features/settings/permission-modes.gen.json"
	permissionTableDocPath  = "../../docs-site/src/content/docs/reference/permission-modes.md"
	permissionTableStart    = "<!-- permission-table:start -->\n"
	permissionTableEnd      = "<!-- permission-table:end -->"
)

func TestPermissionTable_PublishedFilesAreCurrent(t *testing.T) {
	update := os.Getenv("OLV_UPDATE_PERMISSION_TABLE") == "1"

	t.Run("console", func(t *testing.T) {
		if _, err := os.Stat("../../web"); err != nil {
			t.Skip("this tree has no console: the table is checked where it is published")
		}
		want, err := PermissionTableJSON()
		if err != nil {
			t.Fatal(err)
		}
		if update {
			if err := os.WriteFile(permissionTableJSONPath, want, 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		got, err := os.ReadFile(permissionTableJSONPath)
		if err != nil {
			t.Fatalf("read the console table: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is not what the launch builders produce: regenerate it (OLV_UPDATE_PERMISSION_TABLE=1)", permissionTableJSONPath)
		}
	})

	t.Run("docs", func(t *testing.T) {
		if _, err := os.Stat("../../docs-site"); err != nil {
			t.Skip("this tree has no docs-site: the table is checked where the docs are published")
		}
		want := PermissionTableMarkdown()
		doc, err := os.ReadFile(permissionTableDocPath)
		if err != nil {
			t.Fatalf("read the permissions page: %v", err)
		}
		head, rest, found := strings.Cut(string(doc), permissionTableStart)
		got, tail, foundEnd := strings.Cut(rest, permissionTableEnd)
		if !found || !foundEnd {
			t.Fatalf("%s has no %q ... %q block to hold the table", permissionTableDocPath, strings.TrimSpace(permissionTableStart), permissionTableEnd)
		}
		if update {
			page := head + permissionTableStart + want + permissionTableEnd + tail
			if err := os.WriteFile(permissionTableDocPath, []byte(page), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		if got != want {
			t.Errorf("the table in %s is not what the launch builders produce: regenerate it (OLV_UPDATE_PERMISSION_TABLE=1)", permissionTableDocPath)
		}
	})
}

type permissionTableFile struct {
	Tools []struct {
		Driver string `json:"driver"`
		Label  string `json:"label"`
	} `json:"tools"`
	Modes []PermissionRow `json:"modes"`
}

// PermissionTableJSON is the table as the console reads it.
func PermissionTableJSON() ([]byte, error) {
	var f permissionTableFile
	for _, t := range permissionTools {
		f.Tools = append(f.Tools, struct {
			Driver string `json:"driver"`
			Label  string `json:"label"`
		}{t.Driver, t.Label})
	}
	f.Modes = PermissionTable()
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(shortArraysOnOneLine(out), '\n'), nil
}

// stringArray matches a JSON array of plain strings that json.MarshalIndent spread
// over several lines.
var stringArray = regexp.MustCompile(`(?m)^( *)("[a-z_]+": )\[\n((?: +"[^"\\\n]*",?\n)+) *\](,?)$`)

// shortArraysOnOneLine writes an array of strings on one line when the line fits in 80
// columns, which is how the console's formatter lays the file out. Without it the file
// the console commits fails that formatter's check, and the formatter's settings are not
// ours to loosen for a generated file.
func shortArraysOnOneLine(in []byte) []byte {
	const printWidth = 80
	return stringArray.ReplaceAllFunc(in, func(block []byte) []byte {
		m := stringArray.FindSubmatch(block)
		var items []string
		for _, line := range strings.Split(strings.TrimSpace(string(m[3])), "\n") {
			items = append(items, strings.TrimSuffix(strings.TrimSpace(line), ","))
		}
		one := string(m[1]) + string(m[2]) + "[" + strings.Join(items, ", ") + "]" + string(m[4])
		if len(one) > printWidth {
			return block
		}
		return []byte(one)
	})
}

// PermissionTableMarkdown is the table as the documentation prints it.
func PermissionTableMarkdown() string {
	var b strings.Builder
	b.WriteString("| Permission | What it does |")
	for _, t := range permissionTools {
		fmt.Fprintf(&b, " %s |", t.Label)
	}
	b.WriteString("\n|---|---|")
	for range permissionTools {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, row := range PermissionTable() {
		label := "**" + row.Label + "**"
		if row.Default {
			label += " (default)"
		}
		fmt.Fprintf(&b, "| %s<br>`%s` | %s |", label, row.Name, row.Summary)
		for _, cell := range row.Tools {
			text := "`" + strings.Join(cell.Receives, "`, `") + "`"
			if cell.AsksInstead {
				text = "No automatic mode: asks as in Supervised (" + text + ")"
			}
			fmt.Fprintf(&b, " %s |", text)
		}
		b.WriteString("\n")
	}
	return b.String()
}
