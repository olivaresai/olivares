// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package modulespec

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These fresh-install guides call module APIs directly or through the generated
// governed-RAG bootstrap. Check prerequisites against the shipped catalog, not
// a second list of optional modules. Historical version snapshots stay intact.
func TestTutorialDocsEnableOptionalModulesBeforeUse(t *testing.T) {
	routes := regexp.MustCompile(`/v1/m/([a-z0-9-]+)/`)
	specs := map[string]Spec{}
	for _, spec := range All() {
		specs[spec.Namespace] = spec
	}
	root := filepath.Join("..", "..")
	bootstrap, err := os.ReadFile(filepath.Join(root, "cmd", "olivares", "cmd_quickstart_governed_rag.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"", "de", "es", "fr", "ja", "ru", "zh"} {
		for _, page := range []string{
			"tutorials/getting-started/single-node.mdx",
			"how-to/cookbook/drift-triage.md",
			"how-to/build-a-workflow.md",
			"how-to/governed-data-for-claude.md",
		} {
			t.Run(filepath.Join(locale, page), func(t *testing.T) {
				path := filepath.Join(root, "docs-site", "src", "content", "docs", locale, page)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				text := string(data)
				// Expand the executable invocation only; the earlier file-description
				// table mentions the script without asking the reader to run it.
				if strings.HasSuffix(page, "governed-data-for-claude.md") {
					invocation := "/var/lib/olivares/quickstart/governed-rag/bootstrap-after-login.sh"
					if !strings.Contains(text, invocation) {
						t.Fatal("missing governed-RAG bootstrap invocation")
					}
					text = strings.ReplaceAll(text, invocation, string(bootstrap))
				}
				matches := routes.FindAllStringSubmatchIndex(text, -1)
				if len(matches) == 0 {
					t.Fatal("guide contains no module API calls")
				}
				checked := map[string]bool{}
				for _, match := range matches {
					ns := text[match[2]:match[3]]
					if checked[ns] {
						continue
					}
					checked[ns] = true
					spec, ok := specs[ns]
					if !ok {
						t.Errorf("API namespace %q is absent from the module catalog", ns)
						continue
					}
					if spec.Standard || spec.Kind == "kernel" {
						continue
					}
					enable := regexp.MustCompile(`olivares modules on ` + regexp.QuoteMeta(ns) + `\b`)
					if !enable.MatchString(text[:match[0]]) {
						t.Errorf("%s is off on fresh installs: document `olivares modules on %s` before its first API call", ns, ns)
					}
				}
			})
		}
	}
}
