// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// cloud-init's status.json and result.json as cloud-init 26.1 writes them under /run/cloud-init:
// each stage's errors, recoverable errors and times, and the datasource and errors of the run.
const (
	cloudInitDone = `{"v1": {"datasource": "DataSourceNoCloud [seed=/var/lib/cloud/seed/nocloud]",
  "init-local": {"errors": [], "recoverable_errors": {}, "start": 1.5, "finished": 2.5},
  "init": {"errors": [], "recoverable_errors": {}, "start": 3.5, "finished": 4.5},
  "modules-config": {"errors": [], "recoverable_errors": {}, "start": 5.5, "finished": 6.5},
  "modules-final": {"errors": [], "recoverable_errors": {}, "start": 7.5, "finished": 8.5},
  "stage": null}}`
	cloudInitResult = `{"v1": {"datasource": "DataSourceNoCloud [seed=/var/lib/cloud/seed/nocloud]", "errors": []}}`
)

// The host-settings stage reads cloud-init's own status files, as cloud-init's status command
// reads them: done, done with recoverable errors, errors, disabled, and not installed. After
// cloud-final.service, an incomplete run refuses instead of waiting. It runs no program.
func TestCloudInitHost_ReadsCloudInitsOwnStatusFilesAndRunsNoProgram(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	recoverable := strings.Replace(cloudInitDone, `"modules-config": {"errors": [], "recoverable_errors": {}`,
		`"modules-config": {"errors": [], "recoverable_errors": {"WARNING": ["a deprecated key"]}`, 1)
	failed := strings.Replace(cloudInitDone, `"modules-final": {"errors": []`,
		`"modules-final": {"errors": ["a module failed"]`, 1)
	malformedErrors := strings.Replace(cloudInitDone, `"modules-final": {"errors": []`,
		`"modules-final": {"errors": "a module failed"`, 1)
	malformedStart := strings.Replace(failed, `"start": 7.5`, `"start": "7.5"`, 1)
	status, result := "run/cloud-init/status.json", "run/cloud-init/result.json"
	cases := []struct {
		name   string
		files  map[string]string
		state  State // "" when the stage records its effect
		reason string
	}{
		{"done", map[string]string{status: cloudInitDone, result: cloudInitResult}, "", "cloud-init done"},
		{"done with recoverable errors, as the command's exit 2", map[string]string{status: recoverable, result: cloudInitResult}, "", "cloud-init done"},
		{"an error in a stage", map[string]string{status: failed, result: cloudInitResult}, Refused, "cloud-init reported errors"},
		{"an error while running", map[string]string{status: failed}, Refused, "cloud-init reported errors"},
		{"final stage incomplete: a status and no result", map[string]string{status: cloudInitDone}, Refused, "cloud-init's final stage did not complete; its own log names the failure"},
		{"not started: neither file", map[string]string{}, Refused, "cloud-init did not run in this boot"},
		{"a result without a status", map[string]string{result: cloudInitResult}, Refused, "the cloud-init status cannot be read"},
		{"disabled by the marker file", map[string]string{"etc/cloud/cloud-init.disabled": ""}, Refused, "is disabled"},
		{"enabled on the kernel command line over the marker file", map[string]string{
			"etc/cloud/cloud-init.disabled": "", "proc/cmdline": "ro quiet cloud-init=enabled\n", status: cloudInitDone, result: cloudInitResult}, "", "cloud-init done"},
		{"disabled on the kernel command line", map[string]string{"proc/cmdline": "ro quiet cloud-init=disabled\n"}, Refused, "is disabled"},
		{"disabled by its generator", map[string]string{"run/cloud-init/disabled": ""}, Refused, "is disabled"},
		{"a status that is not JSON", map[string]string{status: "{", result: cloudInitResult}, Refused, "the cloud-init status cannot be read"},
		{"a stage whose errors is a string", map[string]string{status: malformedErrors, result: cloudInitResult}, Refused, "the cloud-init status cannot be read"},
		{"a stage whose start is a string with an error", map[string]string{status: malformedStart, result: cloudInitResult}, Refused, "the cloud-init status cannot be read"},
		{"not installed", map[string]string{"(no program)": "", status: cloudInitDone, result: cloudInitResult}, Refused, "is not installed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// The host settings the answers declare, so only cloud-init's state decides.
			place(t, root, "proc/sys/kernel/hostname", in.Answers.Hostname+"\n")
			place(t, root, "root/.ssh/authorized_keys", strings.Join(in.Answers.SSHAuthorizedKeys, "\n")+"\n")
			if _, absent := tc.files["(no program)"]; !absent {
				place(t, root, "usr/bin/cloud-init", "#!/usr/bin/python3\n")
			}
			for path, content := range tc.files {
				if path != "(no program)" {
					place(t, root, path, content)
				}
			}
			programs := &noProgram{}
			effect, err := CloudInitHost{Host: Host{Root: root, Run: programs.run}, Network: func(context.Context) ([]NetworkProfile, error) {
				return []NetworkProfile{{Interface: "ens4", Managed: true, Filename: "/etc/NetworkManager/system-connections/cloud-init-ens4.nmconnection"}}, nil
			}}.Apply(context.Background(), in)
			if len(programs.asked) != 0 {
				t.Fatalf("reading cloud-init's state ran %q", programs.asked)
			}
			var outcome *Outcome
			switch {
			case tc.state == "" && (err != nil || !strings.Contains(string(effect), tc.reason)):
				t.Fatalf("got %q %v, want an effect naming %q", effect, err, tc.reason)
			case tc.state != "" && (!errors.As(err, &outcome) || outcome.State != tc.state || !strings.Contains(outcome.Reason, tc.reason)):
				t.Fatalf("got %q %v, want %s naming %q", effect, err, tc.state, tc.reason)
			}
		})
	}
}

// No observe path of first boot runs a program. Each stage's observe method, and every function
// and method of this package it reaches, reads files only: a program first boot executed would
// need the SELinux policy to let its domain execute every file of that program's type.
func TestFirstBoot_ObservePathsRunNoProgram(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	observers, found := programsRunFrom(fset, files, "observe")
	if observers < 4 {
		t.Fatalf("read %d observe methods; the stages have at least four", observers)
	}
	for _, finding := range found {
		t.Error(finding)
	}

	// The negative control: a copy of Storage whose observe still asks for `id -u`, one call deep.
	t.Run("a copy whose observe still runs id -u is caught", func(t *testing.T) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "copy.go", `package base
func (s Storage) observe(ctx context.Context, _ Input) (Effect, error) { return s.lookup(ctx) }
func (s Storage) lookup(ctx context.Context) (Effect, error) {
	uid, err := s.Host.Run(ctx, "id", "-u", productAccount)
	return Effect(uid), err
}`, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, found := programsRunFrom(fset, []*ast.File{f}, "observe"); len(found) != 1 {
			t.Fatalf("the check missed a program run one call below observe: %q", found)
		}
	})
	for _, tc := range []struct {
		name string
		body string
	}{
		{"a copy whose observe calls the external runner", `
		uid, err := carriers.ExecRunner(ctx, "id", "-u", productAccount)
		return Effect(uid), err`},
		{"a copy whose observe refers to the external runner", `
		run := carriers.ExecRunner
		uid, err := run(ctx, "id", "-u", productAccount)
		return Effect(uid), err`},
		{"a copy whose observe aliases the host", `
		h := s.Host
		uid, err := h.Run(ctx, "id", "-u", productAccount)
		return Effect(uid), err`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "copy.go", `package base
func (s Storage) observe(ctx context.Context, _ Input) (Effect, error) {`+tc.body+`}`, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, found := programsRunFrom(fset, []*ast.File{f}, "observe"); len(found) != 1 {
				t.Fatalf("the check missed a program run from observe: %q", found)
			}
		})
	}

}

// programsRunFrom follows every method named root, and the functions and methods of the same
// files it calls. It reports references to carriers.ExecRunner and calls to any Run receiver,
// os/exec, os.StartProcess or syscall's exec and fork. It returns how many root methods it read.
func programsRunFrom(fset *token.FileSet, files []*ast.File, root string) (int, []string) {
	funcs := map[string]*ast.FuncDecl{}
	receiverOf := func(fn *ast.FuncDecl) (typ, name string) {
		if fn.Recv == nil || len(fn.Recv.List) == 0 {
			return "", ""
		}
		field := fn.Recv.List[0]
		expr := field.Type
		if star, ok := expr.(*ast.StarExpr); ok {
			expr = star.X
		}
		if id, ok := expr.(*ast.Ident); ok {
			typ = id.Name
		}
		if len(field.Names) > 0 {
			name = field.Names[0].Name
		}
		return typ, name
	}
	var queue []string
	for _, f := range files {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				typ, _ := receiverOf(fn)
				funcs[typ+"."+fn.Name.Name] = fn
				if typ != "" && fn.Name.Name == root {
					queue = append(queue, typ+"."+fn.Name.Name)
				}
			}
		}
	}
	observers := len(queue)
	seen := map[string]bool{}
	var found []string
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if seen[key] {
			continue
		}
		seen[key] = true
		fn := funcs[key]
		typ, self := receiverOf(fn)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if selector, ok := n.(*ast.SelectorExpr); ok {
				if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "carriers" && selector.Sel.Name == "ExecRunner" {
					found = append(found, fmt.Sprintf("%s references a program runner (%s)", key, fset.Position(selector.Pos())))
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if funcs["."+fun.Name] != nil {
					queue = append(queue, "."+fun.Name)
				}
			case *ast.SelectorExpr:
				// Host.Run was already forbidden; foreign or aliased Run receivers are too.
				runs := fun.Sel.Name == "Run"
				switch x := fun.X.(type) {
				case *ast.Ident:
					switch {
					case x.Name == "exec" || (x.Name == "os" && fun.Sel.Name == "StartProcess") ||
						(x.Name == "syscall" && (strings.HasPrefix(fun.Sel.Name, "Exec") || strings.HasPrefix(fun.Sel.Name, "ForkExec"))):
						runs = true
					case x.Name == self && typ == "Host" && fun.Sel.Name == "Run":
						runs = true
					case x.Name == self && funcs[typ+"."+fun.Sel.Name] != nil:
						queue = append(queue, typ+"."+fun.Sel.Name)
					}
				case *ast.SelectorExpr:
					if x.Sel.Name == "Host" {
						if fun.Sel.Name == "Run" {
							runs = true
						} else if funcs["Host."+fun.Sel.Name] != nil {
							queue = append(queue, "Host."+fun.Sel.Name)
						}
					}
				}
				if runs {
					found = append(found, fmt.Sprintf("%s runs a program (%s)", key, fset.Position(call.Pos())))
				}
			}
			return true
		})
	}
	return observers, found
}
