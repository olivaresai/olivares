// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package scaffold_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/scaffold"
)

// sdkDir locates this repo's sdk/ directory from the test file's own location
// (sdk/scaffold/scaffold_test.go → two levels up is sdk/). The compile tests
// hand it to Generate as SDKPath so the generated repo builds hermetically and
// offline against the zero-dep SDK checkout, with no network and no go.sum.
func sdkDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate the sdk/ checkout")
	}
	d := filepath.Dir(filepath.Dir(thisFile)) // …/sdk/scaffold → …/sdk
	if _, err := os.Stat(filepath.Join(d, "go.mod")); err != nil {
		t.Fatalf("derived sdk dir %q has no go.mod: %v", d, err)
	}
	return d
}

// validOpts is the baseline every validation case mutates: a fresh empty
// target dir and a well-formed source connector.
func validOpts(t *testing.T) scaffold.Options {
	t.Helper()
	return scaffold.Options{
		Dir:    filepath.Join(t.TempDir(), "repo"),
		Name:   "acme.widget-audit",
		Module: "example.com/acme/widget-audit",
		Kind:   scaffold.KindSource,
	}
}

// goEnv is os.Environ plus GOWORK=off: the generated repo is a standalone
// module and must never resolve through this repo's go.work (the environment
// here exports GOWORK explicitly, so appending the override is mandatory —
// last entry wins).
func goEnv() []string {
	return append(os.Environ(), "GOWORK=off")
}

func TestGenerateValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*scaffold.Options)
		wantSub string // substring the refusal must carry (precise errors are part of the contract)
	}{
		{"one-part name", func(o *scaffold.Options) { o.Name = "widgetaudit" }, "exactly two"},
		{"uppercase name", func(o *scaffold.Options) { o.Name = "Acme.widget" }, "lowercase"},
		{"empty name", func(o *scaffold.Options) { o.Name = "" }, "two non-empty"},
		{"empty connector part", func(o *scaffold.Options) { o.Name = "acme." }, "two non-empty"},
		{"empty vendor part", func(o *scaffold.Options) { o.Name = ".widget" }, "two non-empty"},
		{"three parts", func(o *scaffold.Options) { o.Name = "acme.widget.audit" }, "exactly two"},
		{"digit-led package", func(o *scaffold.Options) { o.Name = "acme.9lives" }, "valid Go package name"},
		// Derived package name must not be a Go keyword / main / plugin: each
		// would generate a non-compiling (or, for plugin, colliding) repo.
		{"keyword package func", func(o *scaffold.Options) { o.Name = "acme.func" }, "Go keyword"},
		{"keyword package range", func(o *scaffold.Options) { o.Name = "acme.range" }, "Go keyword"},
		{"keyword package via hyphens", func(o *scaffold.Options) { o.Name = "acme.ty-pe" }, "Go keyword"},
		{"package main", func(o *scaffold.Options) { o.Name = "acme.main" }, "cannot be named"},
		{"package plugin", func(o *scaffold.Options) { o.Name = "acme.plugin" }, "collides with"},
		// Leading/trailing/pure hyphens in a name part break BinName and the
		// `go build -o <bin>` invocation; refuse them up front.
		{"leading-hyphen connector", func(o *scaffold.Options) { o.Name = "acme.-widget" }, "exactly two"},
		{"trailing-hyphen connector", func(o *scaffold.Options) { o.Name = "acme.widget-" }, "exactly two"},
		{"leading-hyphen vendor", func(o *scaffold.Options) { o.Name = "-acme.widget" }, "exactly two"},
		{"pure-hyphen connector", func(o *scaffold.Options) { o.Name = "acme.-" }, "exactly two"},
		{"sdk path with space", func(o *scaffold.Options) { o.SDKPath = "/has a space/sdk" }, "whitespace"},
		{"bad kind", func(o *scaffold.Options) { o.Kind = "module" }, "Kind"},
		{"empty kind", func(o *scaffold.Options) { o.Kind = "" }, "Kind"},
		{"bad template", func(o *scaffold.Options) { o.Template = "metrics-drain" }, "Template"},
		{"conflicting template and kind", func(o *scaffold.Options) { o.Template = scaffold.TemplateContentSource }, "conflicts"},
		{"missing module", func(o *scaffold.Options) { o.Module = "" }, "Module is required"},
		{"module with space", func(o *scaffold.Options) { o.Module = "example.com/acme x" }, "whitespace"},
		{"missing dir", func(o *scaffold.Options) { o.Dir = "" }, "Dir is required"},
		{"sdk path nonexistent", func(o *scaffold.Options) { o.SDKPath = filepath.Join(o.Dir, "no-such-checkout") }, "not a checkout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := validOpts(t)
			tc.mutate(&o)
			err := scaffold.Generate(o)
			if err == nil {
				t.Fatalf("Generate accepted unusable input; want a refusal containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("refusal %q does not name the problem; want substring %q", err, tc.wantSub)
			}
			// Deny-closed means refuse BEFORE writing: the target must stay absent/empty.
			if entries, rdErr := os.ReadDir(o.Dir); rdErr == nil && len(entries) > 0 {
				t.Fatalf("Generate wrote %d entries into %s despite refusing", len(entries), o.Dir)
			}
		})
	}
}

// TestGenerateValidationSDKPathNotSDK covers the supplied-but-unusable SDKPath
// shapes separately (they need fixture dirs, not just field mutations): a dir
// with no go.mod, a dir that is some OTHER module, and — with WithPlugin — an
// SDK checkout missing the plugin submodule.
func TestGenerateValidationSDKPathNotSDK(t *testing.T) {
	t.Run("dir without go.mod", func(t *testing.T) {
		o := validOpts(t)
		o.SDKPath = t.TempDir()
		if err := scaffold.Generate(o); err == nil || !strings.Contains(err.Error(), "not a checkout") {
			t.Fatalf("want 'not a checkout' refusal, got %v", err)
		}
	})
	t.Run("wrong module", func(t *testing.T) {
		o := validOpts(t)
		fake := t.TempDir()
		if err := os.WriteFile(filepath.Join(fake, "go.mod"), []byte("module example.com/not-the-sdk\n\ngo 1.26.3\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		o.SDKPath = fake
		if err := scaffold.Generate(o); err == nil || !strings.Contains(err.Error(), "not the SDK module") {
			t.Fatalf("want 'not the SDK module' refusal, got %v", err)
		}
	})
	t.Run("missing plugin submodule under WithPlugin", func(t *testing.T) {
		o := validOpts(t)
		fake := t.TempDir()
		if err := os.WriteFile(filepath.Join(fake, "go.mod"), []byte("module github.com/olivaresai/olivares/sdk\n\ngo 1.26.3\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		o.SDKPath = fake
		o.WithPlugin = true
		if err := scaffold.Generate(o); err == nil || !strings.Contains(err.Error(), "plugin submodule") {
			t.Fatalf("want 'plugin submodule' refusal, got %v", err)
		}
	})
	// A substring match would accept the sibling sdk/plugin checkout (its go.mod
	// declares "module .../sdk/plugin" ⊃ ".../sdk"); the exact module-directive
	// scan must REFUSE it.
	t.Run("sdk-path points at sdk/plugin", func(t *testing.T) {
		o := validOpts(t)
		o.SDKPath = filepath.Join(sdkDir(t), "plugin")
		if _, err := os.Stat(filepath.Join(o.SDKPath, "go.mod")); err != nil {
			t.Skipf("sdk/plugin checkout not present: %v", err)
		}
		if err := scaffold.Generate(o); err == nil || !strings.Contains(err.Error(), "not the SDK module") {
			t.Fatalf("want 'not the SDK module' refusal for sdk/plugin path, got %v", err)
		}
	})
	// A go.mod that only MENTIONS the SDK path (in a comment) but does not
	// declare it as the module must also be refused.
	t.Run("module path only in a comment", func(t *testing.T) {
		o := validOpts(t)
		fake := t.TempDir()
		gomod := "// see github.com/olivaresai/olivares/sdk\nmodule example.com/not-the-sdk\n\ngo 1.26.3\n"
		if err := os.WriteFile(filepath.Join(fake, "go.mod"), []byte(gomod), 0o644); err != nil {
			t.Fatal(err)
		}
		o.SDKPath = fake
		if err := scaffold.Generate(o); err == nil || !strings.Contains(err.Error(), "not the SDK module") {
			t.Fatalf("want 'not the SDK module' refusal for comment-only mention, got %v", err)
		}
	})
}

func TestGenerateRefusesNonEmptyDir(t *testing.T) {
	o := validOpts(t)
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(o.Dir, "precious.txt")
	if err := os.WriteFile(precious, []byte("user work"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := scaffold.Generate(o)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("want 'not empty' refusal, got %v", err)
	}
	// The user's file must be untouched.
	b, rdErr := os.ReadFile(precious)
	if rdErr != nil || string(b) != "user work" {
		t.Fatalf("refusal damaged existing work: %v %q", rdErr, b)
	}
}

// assertBoundaryByConstruction walks the generated tree and fails if any file
// references an upstream AGPL module path. The generated scripts/check-boundary.sh
// is the single exception: naming the forbidden core prefix is its entire job.
func assertBoundaryByConstruction(t *testing.T, dir string) {
	t.Helper()
	forbidden := [][]byte{
		[]byte("github.com/olivaresai/olivares/core"),
		[]byte("github.com/olivaresai/olivares/connectors"),
		[]byte("github.com/olivaresai/olivares/modules"),
	}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if filepath.Base(path) == "check-boundary.sh" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, bad := range forbidden {
			if bytes.Contains(b, bad) {
				t.Errorf("generated file %s references %s (boundary breach by construction)", path, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking generated tree: %v", err)
	}
}

func TestGenerateSourceWithPlugin(t *testing.T) {
	o := validOpts(t)
	o.WithPlugin = true // SDKPath deliberately unset: the go.mod note must appear instead of replaces
	if err := scaffold.Generate(o); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for _, rel := range []string{
		"go.mod",
		"widgetaudit.go",
		"widgetaudit_test.go",
		"cmd/acme-widget-audit/main.go",
		"README.md",
		"scripts/check-boundary.sh",
		".gitignore",
	} {
		if _, err := os.Stat(filepath.Join(o.Dir, rel)); err != nil {
			t.Errorf("expected generated file %s: %v", rel, err)
		}
	}

	// The boundary script must be executable.
	if info, err := os.Stat(filepath.Join(o.Dir, "scripts", "check-boundary.sh")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("check-boundary.sh is not executable: %v", info.Mode())
	}

	gomod, err := os.ReadFile(filepath.Join(o.Dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(gomod, []byte("github.com/olivaresai/olivares/sdk v0.0.0")) {
		t.Error("go.mod lacks the sdk require")
	}
	if !bytes.Contains(gomod, []byte("github.com/olivaresai/olivares/sdk/plugin v0.0.0")) {
		t.Error("go.mod lacks the sdk/plugin require (WithPlugin is set)")
	}
	// Replace directives appear IFF SDKPath was supplied — here it was not:
	// the clearly-commented note must guide the author instead.
	if bytes.Contains(gomod, []byte("\nreplace ")) {
		t.Error("go.mod carries replace directives although SDKPath was not set")
	}
	if !bytes.Contains(gomod, []byte("// NOTE: until the first public tags")) {
		t.Error("go.mod lacks the commented replace-to-your-checkout note")
	}

	maingo, err := os.ReadFile(filepath.Join(o.Dir, "cmd", "acme-widget-audit", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(maingo, []byte("sdkplugin.ServeSource(widgetaudit.New())")) {
		t.Errorf("plugin main does not serve the source connector:\n%s", maingo)
	}

	conn, err := os.ReadFile(filepath.Join(o.Dir, "widgetaudit.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"package widgetaudit",
		`const Name = "acme.widget-audit"`,
		"var _ sdk.SourceConnector = (*Source)(nil)",
		"TODO: replace with your system's real facts",
		"SPDX-License-Identifier: Apache-2.0",
	} {
		if !bytes.Contains(conn, []byte(want)) {
			t.Errorf("generated connector lacks %q", want)
		}
	}

	assertBoundaryByConstruction(t, o.Dir)
}

func TestGenerateContentSourceWithPlugin(t *testing.T) {
	o := scaffold.Options{
		Dir:        filepath.Join(t.TempDir(), "repo"),
		Name:       "acme.knowledge-docs",
		Module:     "example.com/acme/knowledge-docs",
		Template:   scaffold.TemplateContentSource,
		WithPlugin: true,
	}
	if err := scaffold.Generate(o); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, rel := range []string{
		"go.mod",
		"knowledgedocs.go",
		"knowledgedocs_test.go",
		"cmd/acme-knowledge-docs/main.go",
		"README.md",
		"scripts/check-boundary.sh",
		".gitignore",
	} {
		if _, err := os.Stat(filepath.Join(o.Dir, rel)); err != nil {
			t.Errorf("expected generated file %s: %v", rel, err)
		}
	}
	maingo, err := os.ReadFile(filepath.Join(o.Dir, "cmd", "acme-knowledge-docs", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(maingo, []byte("sdkplugin.ServeContentSource(knowledgedocs.New())")) {
		t.Errorf("plugin main does not serve the content source:\n%s", maingo)
	}
	conn, err := os.ReadFile(filepath.Join(o.Dir, "knowledgedocs.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"var _ sdk.ContentSource = (*ContentSource)(nil)",
		"Type:        sdk.TypeContentSource",
		`"knowledge.document"`,
		"DeltaContentSource",
	} {
		if !bytes.Contains(conn, []byte(want)) {
			t.Errorf("generated content source lacks %q", want)
		}
	}
	assertBoundaryByConstruction(t, o.Dir)
}

func TestGenerateOutputNoPlugin(t *testing.T) {
	o := scaffold.Options{
		Dir:     filepath.Join(t.TempDir(), "repo"),
		Name:    "acme.pager-bridge",
		Module:  "example.com/acme/pager-bridge",
		Kind:    scaffold.KindOutput,
		SDKPath: sdkDir(t), // exercises the replace-emitting path
	}
	if err := scaffold.Generate(o); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for _, rel := range []string{
		"go.mod",
		"pagerbridge.go",
		"pagerbridge_test.go",
		"README.md",
		"scripts/check-boundary.sh",
		".gitignore",
	} {
		if _, err := os.Stat(filepath.Join(o.Dir, rel)); err != nil {
			t.Errorf("expected generated file %s: %v", rel, err)
		}
	}
	// No plugin requested → no cmd tree, no main.go anywhere.
	if _, err := os.Stat(filepath.Join(o.Dir, "cmd")); !os.IsNotExist(err) {
		t.Errorf("cmd/ generated although WithPlugin=false (stat err: %v)", err)
	}

	gomod, err := os.ReadFile(filepath.Join(o.Dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(gomod, []byte("replace github.com/olivaresai/olivares/sdk => ")) {
		t.Error("go.mod lacks the sdk replace although SDKPath was set")
	}
	if bytes.Contains(gomod, []byte("github.com/olivaresai/olivares/sdk/plugin")) {
		t.Error("go.mod references sdk/plugin although WithPlugin=false")
	}

	conn, err := os.ReadFile(filepath.Join(o.Dir, "pagerbridge.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"package pagerbridge",
		`const Name = "acme.pager-bridge"`,
		"var _ sdk.OutputConnector = (*Output)(nil)",
		"TODO: replace with delivery to your system",
	} {
		if !bytes.Contains(conn, []byte(want)) {
			t.Errorf("generated connector lacks %q", want)
		}
	}

	assertBoundaryByConstruction(t, o.Dir)
}

// TestGeneratedTemplateMatrixCompiles is THE compile matrix: every archetype
// repo must build AND pass its own lifecycle test, hermetically and offline.
// With WithPlugin=false the only dependency is the zero-dep SDK, replaced to
// this repo's checkout — no network, no go.sum entries, no proxy. GOWORK=off
// keeps the workspace out of the resolution.
func TestGeneratedTemplateMatrixCompiles(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	cases := []struct {
		template  string
		name      string
		wantFiles []string
		wantBody  string
	}{
		{scaffold.TemplateContentSource, "acme.knowledge-docs", []string{"knowledgedocs.go", "knowledgedocs_test.go"}, "sdk.ContentSource"},
		{scaffold.TemplateAccessEdgeSource, "acme.access-edge", []string{"accessedge.go", "accessedge_test.go"}, "model.EdgeObservation"},
		{scaffold.TemplateOutputSink, "acme.notify-sink", []string{"notifysink.go", "notifysink_test.go"}, "sdk.OutputConnector"},
		{scaffold.TemplateAgentSurface, "acme.agent-runtime", []string{"agentruntime.go", "agentruntime_test.go"}, "model.FindingReport"},
		{scaffold.TemplateModelProvider, "acme.model-meter", []string{"modelmeter.go", "modelmeter_test.go"}, "model.CostSample"},
	}
	for _, tc := range cases {
		t.Run(tc.template, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "repo")
			err := scaffold.Generate(scaffold.Options{
				Dir:      dir,
				Name:     tc.name,
				Module:   "example.com/" + strings.ReplaceAll(tc.name, ".", "/"),
				Template: tc.template,
				SDKPath:  sdkDir(t),
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			for _, rel := range append([]string{"go.mod", "README.md", "scripts/check-boundary.sh", ".gitignore"}, tc.wantFiles...) {
				if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
					t.Fatalf("expected generated file %s: %v", rel, err)
				}
			}
			body, err := os.ReadFile(filepath.Join(dir, tc.wantFiles[0]))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(body, []byte(tc.wantBody)) {
				t.Fatalf("generated connector lacks %q:\n%s", tc.wantBody, body)
			}
			assertBoundaryByConstruction(t, dir)
			for _, args := range [][]string{
				{"build", "./..."},
				{"test", "./..."},
			} {
				cmd := exec.Command("go", args...)
				cmd.Dir = dir
				cmd.Env = goEnv()
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("go %s in the generated repo failed: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
		})
	}
}

// TestGeneratedBoundaryScriptPasses runs the generated scripts/check-boundary.sh
// in the generated repo (go list -deps — offline, GOWORK=off) and asserts the
// freshly scaffolded tree passes its own boundary check.
func TestGeneratedBoundaryScriptPasses(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not on PATH: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "repo")
	err := scaffold.Generate(scaffold.Options{
		Dir:     dir,
		Name:    "acme.widget-audit",
		Module:  "example.com/acme/widget-audit",
		Kind:    scaffold.KindSource,
		SDKPath: sdkDir(t),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cmd := exec.Command("bash", filepath.Join("scripts", "check-boundary.sh"))
	cmd.Dir = dir
	cmd.Env = goEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated check-boundary.sh failed on a clean scaffold: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("Boundary check OK")) {
		t.Fatalf("boundary script exited 0 without the OK verdict:\n%s", out)
	}
}
