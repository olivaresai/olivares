// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func cmdTestSDKDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate sdk checkout")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	sdk := filepath.Join(root, "sdk")
	if _, err := os.Stat(filepath.Join(sdk, "go.mod")); err != nil {
		t.Fatalf("derived sdk dir %q has no go.mod: %v", sdk, err)
	}
	return sdk
}

func runConnectorInit(args ...string) (string, error) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestConnectorInitGeneratesTemplates(t *testing.T) {
	cases := []struct {
		name       string
		template   string
		module     string
		dirName    string
		goFile     string
		testFile   string
		mainFile   string
		wantMain   string
		wantGoBody []string
	}{
		{
			name:     "acme.knowledge-docs",
			template: "content-source",
			module:   "example.com/acme/knowledge-docs",
			dirName:  "knowledge",
			goFile:   "knowledgedocs.go",
			testFile: "knowledgedocs_test.go",
			mainFile: filepath.Join("cmd", "acme-knowledge-docs", "main.go"),
			wantMain: "sdkplugin.ServeContentSource(knowledgedocs.New())",
			wantGoBody: []string{
				`const Name = "acme.knowledge-docs"`,
				"Type:        sdk.TypeContentSource",
				`"knowledge.document"`,
			},
		},
		{
			name:     "acme.model-meter",
			template: "model-provider",
			module:   "example.com/acme/model-meter",
			dirName:  "model",
			goFile:   "modelmeter.go",
			testFile: "modelmeter_test.go",
			mainFile: filepath.Join("cmd", "acme-model-meter", "main.go"),
			wantMain: "sdkplugin.ServeSource(modelmeter.New())",
			wantGoBody: []string{
				`const Name = "acme.model-meter"`,
				"model.CostSample",
				`"observation.cost"`,
				`"observation.edge"`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.template, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tc.dirName)
			out, err := runConnectorInit(
				"connector", "init", tc.name,
				"--dir", dir,
				"--module", tc.module,
				"--template", tc.template,
				"--sdk-path", cmdTestSDKDir(t),
			)
			if err != nil {
				t.Fatalf("connector init: %v\n%s", err, out)
			}
			for _, rel := range []string{
				"go.mod",
				tc.goFile,
				tc.testFile,
				tc.mainFile,
				"README.md",
				filepath.Join("scripts", "check-boundary.sh"),
				".gitignore",
			} {
				if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
					t.Fatalf("expected generated file %s: %v", rel, err)
				}
			}
			gomod := readConnectorGeneratedFile(t, dir, "go.mod")
			for _, want := range []string{
				"module " + tc.module,
				"replace github.com/olivaresai/olivares/sdk => ",
				"replace github.com/olivaresai/olivares/sdk/plugin => ",
			} {
				if !strings.Contains(gomod, want) {
					t.Fatalf("go.mod missing %q:\n%s", want, gomod)
				}
			}
			mainGo := readConnectorGeneratedFile(t, dir, tc.mainFile)
			if !strings.Contains(mainGo, tc.wantMain) {
				t.Fatalf("plugin main missing %q:\n%s", tc.wantMain, mainGo)
			}
			connGo := readConnectorGeneratedFile(t, dir, tc.goFile)
			for _, want := range tc.wantGoBody {
				if !strings.Contains(connGo, want) {
					t.Fatalf("%s missing %q:\n%s", tc.goFile, want, connGo)
				}
			}
			readme := readConnectorGeneratedFile(t, dir, "README.md")
			for _, want := range []string{
				"**" + tc.template + "**",
				"## Governed by construction",
				"Enforcement is engine-side by construction",
				"You write zero governance code",
			} {
				if !strings.Contains(readme, want) {
					t.Fatalf("README missing %q:\n%s", want, readme)
				}
			}
			if tc.template == "model-provider" && !strings.Contains(readme, "Model-access") {
				t.Fatalf("model-provider README lacks the model-governance note:\n%s", readme)
			}
			if !strings.Contains(out, "generated "+tc.template+" connector") {
				t.Fatalf("unexpected command output:\n%s", out)
			}
		})
	}
}

func TestConnectorInitSurfacesNonEmptyDirRefusal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("do not overwrite"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runConnectorInit(
		"connector", "init", "acme.widget-audit",
		"--dir", dir,
		"--module", "example.com/acme/widget-audit",
		"--template", "access-edge-source",
		"--sdk-path", cmdTestSDKDir(t),
	)
	if err == nil {
		t.Fatalf("connector init accepted a non-empty dir:\n%s", out)
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("refusal does not surface non-empty dir: %v\n%s", err, out)
	}
}

func readConnectorGeneratedFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}
