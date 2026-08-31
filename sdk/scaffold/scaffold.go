// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"
)

// templatesFS embeds the generator's file templates so the scaffold is a
// single self-contained library/binary with no runtime data directory.
//
//go:embed templates/*.tmpl
var templatesFS embed.FS

// tmplDelims are the template action delimiters. The default {{ }} cannot be
// used because the templates EMIT Go source, where `}}` occurs naturally (e.g.
// a composite literal closing a call argument); [[ ]] never appears in the
// generated Go, shell, JSON or Markdown, so templates stay literal-clean.
const (
	tmplDelimLeft  = "[["
	tmplDelimRight = "]]"
)

// The connector kinds Generate accepts — the two ecosystem-facing component
// types (sdk.TypeSource / sdk.TypeOutput). Modules are first-party product
// logic and are not scaffolded here.
const (
	// KindSource scaffolds an sdk.SourceConnector (gathers facts, emits
	// observations).
	KindSource = "source"
	// KindOutput scaffolds an sdk.OutputConnector (delivers notifications).
	KindOutput = "output"
)

// The archetype templates Generate accepts through Options.Template. They are
// presets over the stable SDK surfaces, not new author contracts.
const (
	TemplateContentSource    = "content-source"
	TemplateAccessEdgeSource = "access-edge-source"
	TemplateOutputSink       = "output-sink"
	TemplateAgentSurface     = "agent-surface"
	TemplateModelProvider    = "model-provider"
)

var templateOrder = []string{
	TemplateContentSource,
	TemplateAccessEdgeSource,
	TemplateOutputSink,
	TemplateAgentSurface,
	TemplateModelProvider,
}

// Templates returns the closed set of scaffold archetype template names.
func Templates() []string {
	out := make([]string, len(templateOrder))
	copy(out, templateOrder)
	return out
}

// Options is the input to Generate. Every field except WithPlugin and SDKPath
// is required; Generate validates deny-closed and refuses unusable input with
// a precise error rather than generating something broken.
type Options struct {
	// Dir is the target directory for the generated repository. It is created
	// if absent; an EXISTING NON-EMPTY directory is refused outright (the
	// scaffold never overwrites user work).
	Dir string
	// Name is the connector's Descriptor name: "<vendor>.<connector>", exactly
	// two non-empty lowercase [a-z0-9-] parts (e.g. "acme.widget-audit"). The
	// connector part, hyphens stripped, must yield a valid Go package name.
	Name string
	// Module is the generated repository's Go module path (e.g.
	// "github.com/acme/olivares-connector-widget-audit"). Required; must not
	// contain whitespace.
	Module string
	// Kind selects what to scaffold: KindSource or KindOutput.
	// Deprecated selector for back-compat: leave Template empty to keep the
	// original source/output scaffold behavior used by olivares-connector-new.
	Kind string
	// Template selects one of the five connector archetype presets. Empty means
	// "use Kind", preserving the original source/output Generate API exactly.
	Template string
	// WithPlugin additionally emits cmd/<vendor-connector>/main.go calling
	// plugin.ServeSource/ServeOutput/ServeContentSource, and the sdk/plugin
	// require in go.mod, so the repo builds a shippable out-of-process plugin
	// binary.
	WithPlugin bool
	// SDKPath is an optional DEV path to a checkout of the upstream repo's
	// sdk/ directory. When set, the generated go.mod carries replace
	// directives for sdk (and sdk/plugin when WithPlugin) pointing at it, so
	// the repo builds immediately and offline. When unset, go.mod and the
	// README carry a clearly-commented note that, until the first public sdk
	// tags are published, the author adds the replace to their own checkout.
	// A supplied-but-unusable path is refused: it must declare exactly
	// `module github.com/olivaresai/olivares/sdk` (a substring/sibling match is
	// not enough), must contain no whitespace (it is emitted unquoted into the
	// go.mod replace directive), and — with WithPlugin — its sibling plugin/
	// must declare exactly the sdk/plugin module.
	SDKPath string
}

var (
	// namePartRe validates one dotted-name part: non-empty lowercase
	// [a-z0-9-] that neither begins nor ends with a hyphen — the Descriptor
	// naming rule (sdk/component.go). Leading/trailing hyphens are forbidden
	// because BinName (Name with the dot turned to a hyphen) feeds the
	// `go build -o <BinName>` invocation and the cmd/<BinName> dir; an edge
	// hyphen (or a pure-hyphen part) would yield a malformed output name.
	namePartRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	// goPkgRe validates the derived Go package name (connector part with
	// hyphens stripped): a lowercase identifier starting with a letter.
	goPkgRe = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
)

// data is the fully resolved, validated template input derived from Options.
// Everything templates consume is pre-computed here so templates hold zero
// logic beyond conditionals on Kind/WithPlugin/SDKPath.
type data struct {
	// Name is the dotted Descriptor name ("acme.widget-audit").
	Name string
	// Vendor and Connector are the two parts of Name.
	Vendor    string
	Connector string
	// Package is the generated Go package name: Connector with hyphens
	// stripped ("widgetaudit"). Also the base of the two generated .go files.
	Package string
	// Title is a human label derived from Connector ("widget audit").
	Title string
	// Module is the generated repo's module path.
	Module string
	// Kind is KindSource or KindOutput; IsSource is its template-friendly form.
	Kind            string
	Template        string
	IsSource        bool
	IsOutput        bool
	IsContentSource bool
	IsAccessEdge    bool
	IsOutputSink    bool
	IsAgentSurface  bool
	IsModelProvider bool
	Surfaces        []string
	// WithPlugin mirrors Options.WithPlugin.
	WithPlugin bool
	// BinName is the plugin binary / cmd dir name: Name with the dot replaced
	// by a hyphen ("acme-widget-audit").
	BinName string
	// SDKPath / SDKPluginPath are the ABSOLUTE replace targets ("" when the
	// scaffold was invoked without a dev SDK checkout).
	SDKPath       string
	SDKPluginPath string
	// Year is the current year, seeded into the generated SPDX headers next to
	// the "<your name>" placeholder the author fills in.
	Year int
}

// sdkModulePath is the module the generated repo builds against; resolve
// verifies a supplied SDKPath really is a checkout of it (deny-closed: a
// wrong path is refused at generation time, not discovered at build time).
const sdkModulePath = "github.com/olivaresai/olivares/sdk"

type templateSpec struct {
	kind     string
	surfaces []string
}

func templateSpecFor(name string) (templateSpec, bool) {
	switch name {
	case TemplateContentSource:
		return templateSpec{kind: "content-source", surfaces: []string{"knowledge.document"}}, true
	case TemplateAccessEdgeSource:
		return templateSpec{kind: KindSource, surfaces: []string{"observation.edge"}}, true
	case TemplateOutputSink:
		return templateSpec{kind: KindOutput, surfaces: []string{"notify.sink"}}, true
	case TemplateAgentSurface:
		return templateSpec{kind: KindSource, surfaces: []string{"observation.edge", "observation.finding"}}, true
	case TemplateModelProvider:
		return templateSpec{kind: KindSource, surfaces: []string{"observation.cost", "observation.edge"}}, true
	default:
		return templateSpec{}, false
	}
}

func resolveTemplateAndKind(o Options) (templateName string, spec templateSpec, err error) {
	if strings.TrimSpace(o.Template) == "" {
		switch o.Kind {
		case KindSource:
			templateName = TemplateAccessEdgeSource
		case KindOutput:
			templateName = TemplateOutputSink
		default:
			return "", templateSpec{}, fmt.Errorf("scaffold: Kind %q is invalid: it must be %q or %q", o.Kind, KindSource, KindOutput)
		}
		spec, _ = templateSpecFor(templateName)
		return templateName, spec, nil
	}
	templateName = strings.TrimSpace(o.Template)
	var ok bool
	spec, ok = templateSpecFor(templateName)
	if !ok {
		return "", templateSpec{}, fmt.Errorf("scaffold: Template %q is invalid: it must be one of %s", o.Template, strings.Join(templateOrder, ", "))
	}
	if o.Kind != "" && o.Kind != spec.kind {
		return "", templateSpec{}, fmt.Errorf("scaffold: Kind %q conflicts with Template %q (which maps to Kind %q); set only Template or use the matching Kind", o.Kind, templateName, spec.kind)
	}
	return templateName, spec, nil
}

// resolve validates o and derives the template data. Every failure is a
// precise refusal naming the offending field and the rule it broke.
func resolve(o Options) (data, error) {
	var d data
	if strings.TrimSpace(o.Dir) == "" {
		return d, fmt.Errorf("scaffold: Dir is required (the target directory for the generated repository)")
	}
	parts := strings.Split(o.Name, ".")
	if len(parts) != 2 || !namePartRe.MatchString(parts[0]) || !namePartRe.MatchString(parts[1]) {
		return d, fmt.Errorf("scaffold: Name %q is invalid: it must be \"<vendor>.<connector>\" — exactly two non-empty lowercase [a-z0-9-] parts (e.g. \"acme.widget-audit\")", o.Name)
	}
	pkg := strings.ReplaceAll(parts[1], "-", "")
	if !goPkgRe.MatchString(pkg) {
		return d, fmt.Errorf("scaffold: connector part %q of Name does not yield a valid Go package name once hyphens are stripped (got %q; it must start with a letter)", parts[1], pkg)
	}
	// The package name becomes `package <pkg>` in the generated sources, so it
	// cannot be a Go keyword ("package func" is a syntax error). It also cannot
	// be "main" (a root library named main has no func main, so it does not
	// build) nor "plugin" (with -plugin it would collide with the imported
	// sdk/plugin identifier in cmd/<bin>/main.go). Refuse at generation time.
	if token.IsKeyword(pkg) {
		return d, fmt.Errorf("scaffold: connector part %q yields Go package name %q, which is a Go keyword and cannot name a package; choose a different connector name", parts[1], pkg)
	}
	if pkg == "main" {
		return d, fmt.Errorf("scaffold: connector part %q yields Go package name %q; a root library package cannot be named %q (it has no func main); choose a different connector name", parts[1], pkg, pkg)
	}
	if pkg == "plugin" {
		return d, fmt.Errorf("scaffold: connector part %q yields Go package name %q, which collides with the imported sdk/plugin package; choose a different connector name", parts[1], pkg)
	}
	if strings.TrimSpace(o.Module) == "" {
		return d, fmt.Errorf("scaffold: Module is required (the generated repository's Go module path, e.g. \"github.com/acme/widget-audit\")")
	}
	if strings.ContainsAny(o.Module, " \t\r\n") {
		return d, fmt.Errorf("scaffold: Module %q must not contain whitespace", o.Module)
	}
	templateName, spec, err := resolveTemplateAndKind(o)
	if err != nil {
		return d, err
	}

	d = data{
		Name:            o.Name,
		Vendor:          parts[0],
		Connector:       parts[1],
		Package:         pkg,
		Title:           strings.ReplaceAll(parts[1], "-", " "),
		Module:          o.Module,
		Kind:            spec.kind,
		Template:        templateName,
		IsSource:        spec.kind == KindSource,
		IsOutput:        spec.kind == KindOutput,
		IsContentSource: spec.kind == "content-source",
		IsAccessEdge:    templateName == TemplateAccessEdgeSource,
		IsOutputSink:    templateName == TemplateOutputSink,
		IsAgentSurface:  templateName == TemplateAgentSurface,
		IsModelProvider: templateName == TemplateModelProvider,
		Surfaces:        spec.surfaces,
		WithPlugin:      o.WithPlugin,
		BinName:         strings.ReplaceAll(o.Name, ".", "-"),
		Year:            time.Now().Year(),
	}

	if o.SDKPath != "" {
		// go.mod.tmpl emits `replace ... => [[.SDKPath]]` unquoted, so an
		// SDKPath with whitespace yields an unparseable go.mod. Refuse it here
		// (mirrors the Module whitespace refusal — deny-closed at generation
		// time rather than a broken build later).
		if strings.ContainsAny(o.SDKPath, " \t\r\n") {
			return data{}, fmt.Errorf("scaffold: SDKPath %q must not contain whitespace (it is emitted unquoted into the generated go.mod replace directive)", o.SDKPath)
		}
		abs, err := filepath.Abs(o.SDKPath)
		if err != nil {
			return data{}, fmt.Errorf("scaffold: SDKPath %q cannot be made absolute: %w", o.SDKPath, err)
		}
		// filepath.Abs cleans but does not normalize whitespace; an SDKPath that
		// is whitespace-free above stays whitespace-free here, but guard the
		// absolute form too in case Abs prepended a CWD that carries space.
		if strings.ContainsAny(abs, " \t\r\n") {
			return data{}, fmt.Errorf("scaffold: resolved SDKPath %q must not contain whitespace (it is emitted unquoted into the generated go.mod replace directive)", abs)
		}
		if err := requireModuleDir(abs, sdkModulePath); err != nil {
			return data{}, fmt.Errorf("scaffold: SDKPath %q is not the SDK module: %w", o.SDKPath, err)
		}
		d.SDKPath = abs
		if o.WithPlugin {
			// The plugin replace target is the sibling plugin/ checkout
			// (go.mod.tmpl emits `replace .../sdk/plugin => [[.SDKPluginPath]]`).
			// Verify it the same exact way: it must be a checkout of the
			// sdk/plugin module, not merely a directory that exists.
			plug := filepath.Join(abs, "plugin")
			if err := requireModuleDir(plug, sdkModulePath+"/plugin"); err != nil {
				return data{}, fmt.Errorf("scaffold: SDKPath %q has no usable plugin submodule (plugin/), which WithPlugin requires: %w", o.SDKPath, err)
			}
			d.SDKPluginPath = plug
		}
	}
	return d, nil
}

// requireModuleDir verifies dir is a Go module checkout declaring exactly
// `module <want>`. It scans the go.mod line by line and requires an exact
// module directive — a substring match would accept a sibling submodule (e.g.
// .../sdk/plugin's go.mod contains "module .../sdk/plugin" ⊃ ".../sdk") or any
// go.mod that merely mentions the path in a comment. Deny-closed: a wrong path
// is refused at generation time, not discovered at build time. Stdlib only.
func requireModuleDir(dir, want string) error {
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return fmt.Errorf("not a checkout of %s (cannot read its go.mod): %w", want, err)
	}
	for _, line := range strings.Split(string(mod), "\n") {
		if strings.TrimSpace(line) == "module "+want {
			return nil
		}
	}
	return fmt.Errorf("its go.mod does not declare module %s", want)
}

// outFile is one rendered file of the generated tree.
type outFile struct {
	rel  string      // path relative to Options.Dir (always '/'-joined here)
	tmpl string      // template name under templates/
	mode os.FileMode // 0o644 for sources, 0o755 for the shell script
}

// plan returns the file set Generate writes for d, in a stable order.
func plan(d data) []outFile {
	connTmpl, testTmpl := "output.go.tmpl", "output_test.go.tmpl"
	if d.IsSource {
		connTmpl, testTmpl = "source.go.tmpl", "source_test.go.tmpl"
	}
	if d.IsContentSource {
		connTmpl, testTmpl = "content_source.go.tmpl", "content_source_test.go.tmpl"
	}
	files := []outFile{
		{rel: "go.mod", tmpl: "go.mod.tmpl", mode: 0o644},
		{rel: d.Package + ".go", tmpl: connTmpl, mode: 0o644},
		{rel: d.Package + "_test.go", tmpl: testTmpl, mode: 0o644},
		{rel: "README.md", tmpl: "README.md.tmpl", mode: 0o644},
		{rel: filepath.Join("scripts", "check-boundary.sh"), tmpl: "check-boundary.sh.tmpl", mode: 0o755},
		{rel: ".gitignore", tmpl: "gitignore.tmpl", mode: 0o644},
	}
	if d.WithPlugin {
		files = append(files, outFile{rel: filepath.Join("cmd", d.BinName, "main.go"), tmpl: "main.go.tmpl", mode: 0o644})
	}
	return files
}

// Generate validates o and writes a complete out-of-tree connector repository
// into o.Dir. It is deny-closed end to end: unusable input refuses with a
// precise error before anything is written, a non-empty target directory is
// refused outright, and every file is rendered in memory BEFORE the first
// write so a template fault never leaves a half-written tree.
func Generate(o Options) error {
	d, err := resolve(o)
	if err != nil {
		return err
	}

	// Never overwrite user work: an existing target must be an EMPTY directory.
	if entries, err := os.ReadDir(o.Dir); err == nil {
		if len(entries) > 0 {
			return fmt.Errorf("scaffold: target dir %q is not empty; refusing to overwrite existing work (choose a fresh directory)", o.Dir)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("scaffold: target dir %q is unusable: %w", o.Dir, err)
	}

	tmpl, err := template.New("scaffold").Delims(tmplDelimLeft, tmplDelimRight).ParseFS(templatesFS, "templates/*.tmpl")
	if err != nil {
		return fmt.Errorf("scaffold: parsing embedded templates: %w", err)
	}

	// Render everything first…
	files := plan(d)
	rendered := make([][]byte, len(files))
	for i, f := range files {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, f.tmpl, d); err != nil {
			return fmt.Errorf("scaffold: rendering %s: %w", f.rel, err)
		}
		rendered[i] = buf.Bytes()
	}

	// …then write. 0644 for sources, 0755 for the boundary script; the
	// explicit Chmod makes the script executable regardless of umask.
	for i, f := range files {
		abs := filepath.Join(o.Dir, f.rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return fmt.Errorf("scaffold: creating %s: %w", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, rendered[i], f.mode); err != nil {
			return fmt.Errorf("scaffold: writing %s: %w", abs, err)
		}
		if err := os.Chmod(abs, f.mode); err != nil {
			return fmt.Errorf("scaffold: setting mode on %s: %w", abs, err)
		}
	}
	return nil
}
