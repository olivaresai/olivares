// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/gitpublish"
)

// gitpublishBootFunc parses file and returns its function name.
func gitpublishBootFunc(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range parsed.Decls {
		if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("%s: function %s not found", file, name)
	return nil
}

// gitpublishGuarded reports whether call sits in the body of an
// `if set.gitpublish != nil` statement of body.
func gitpublishGuarded(body *ast.BlockStmt, call *ast.CallExpr) bool {
	guarded := false
	ast.Inspect(body, func(node ast.Node) bool {
		conditional, ok := node.(*ast.IfStmt)
		if !ok || guarded {
			return !guarded
		}
		binary, ok := conditional.Cond.(*ast.BinaryExpr)
		if ok && binary.Op == token.NEQ && communicationBootSelectorPath(binary.X) == "set.gitpublish" &&
			communicationBootIdentifier(binary.Y, "nil") &&
			conditional.Body.Pos() <= call.Pos() && call.End() <= conditional.Body.End() {
			guarded = true
		}
		return true
	})
	return guarded
}

// The publication module's ports are bound exactly once in boot(): the
// authority with the serving Authenticator and the composed Authorizer after
// both exist, custody and git after the source roster and the secret store,
// and the sweep on the runtime scheduler, all before the runtime starts and
// under the module's nil guard.
func TestBootBindsGitPublicationOnce(t *testing.T) {
	boot := gitpublishBootFunc(t, "boot.go", "boot")
	assigned := map[string]token.Pos{}
	calls := map[string][]*ast.CallExpr{}
	var sweeps []*ast.CallExpr
	ast.Inspect(boot.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for _, target := range value.Lhs {
				if identifier, ok := target.(*ast.Ident); ok {
					if _, seen := assigned[identifier.Name]; !seen {
						assigned[identifier.Name] = value.End()
					}
				}
			}
		case *ast.CallExpr:
			path := communicationBootSelectorPath(value.Fun)
			calls[path] = append(calls[path], value)
			if path == "rt.SchedulePeriodic" && len(value.Args) == 4 && communicationBootIdentifier(value.Args[0], "gitpublishSweepJobName") {
				sweeps = append(sweeps, value)
			}
		}
		return true
	})
	starts := calls["rt.Start"]
	if len(starts) != 1 {
		t.Fatalf("runtime starts = %d, want one", len(starts))
	}
	for _, name := range []string{"authr", "authz", "secretResolver", "secretStore", "sourceStore"} {
		if assigned[name] == token.NoPos {
			t.Fatalf("boot assigns no %s", name)
		}
	}
	before := func(label string, call *ast.CallExpr, after ...string) {
		t.Helper()
		for _, name := range after {
			if call.Pos() <= assigned[name] {
				t.Errorf("%s precedes the %s assignment", label, name)
			}
		}
		if call.End() >= starts[0].Pos() {
			t.Errorf("%s follows the runtime start", label)
		}
		if !gitpublishGuarded(boot.Body, call) {
			t.Errorf("%s is not under the set.gitpublish nil guard", label)
		}
	}

	authority := calls["set.gitpublish.UseAuthority"]
	if len(authority) != 1 || len(authority[0].Args) != 2 ||
		!communicationBootIdentifier(authority[0].Args[0], "authr") ||
		!communicationBootIdentifier(authority[0].Args[1], "authz") {
		t.Fatalf("gitpublish authority binds = %d, want one UseAuthority(authr, authz)", len(authority))
	}
	before("UseAuthority", authority[0], "authr", "authz")

	custody, git, built := calls["set.gitpublish.UseCustody"], calls["set.gitpublish.UseGit"], calls["newGitPublication"]
	if len(custody) != 1 || len(git) != 1 || len(built) != 1 {
		t.Fatalf("custody binds %d, git binds %d, constructions %d; want one each", len(custody), len(git), len(built))
	}
	if args := built[0].Args; len(args) != 4 || !communicationBootIdentifier(args[1], "sourceStore") ||
		!communicationBootIdentifier(args[2], "secretStore") || !communicationBootIdentifier(args[3], "secretStoreSealerPresent") {
		t.Fatalf("newGitPublication arguments = %#v, want the roster, the sealed secret store and its sealer posture", args)
	}
	before("UseCustody", custody[0], "secretResolver", "secretStore", "sourceStore")
	before("UseGit", git[0], "secretResolver", "secretStore", "sourceStore")

	if len(sweeps) != 1 {
		t.Fatalf("gitpublish sweep registrations = %d, want one", len(sweeps))
	}
	pump, ok := communicationBootCall(sweeps[0].Args[3], "set.gitpublish.SweepPump", 2)
	if !ok || communicationBootSelectorPath(pump.Args[0]) != "st.Leader()" {
		t.Fatalf("sweep job = %#v, want set.gitpublish.SweepPump(st.Leader(), …)", sweeps[0].Args[3])
	}
	if _, ok := communicationBootCall(pump.Args[1], "gitpublishSweepTenants", 2); !ok {
		t.Fatalf("sweep tenants = %#v, want gitpublishSweepTenants(st, log)", pump.Args[1])
	}
	before("the sweep registration", sweeps[0])

	// No other composition file binds a publication port.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "boot.go" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				switch path := communicationBootSelectorPath(call.Fun); {
				case strings.HasSuffix(path, "gitpublish.UseAuthority"), strings.HasSuffix(path, "gitpublish.UseCustody"),
					strings.HasSuffix(path, "gitpublish.UseGit"):
					t.Errorf("%s binds %s outside boot()", file, path)
				}
			}
			return true
		})
	}
}

// buildModules constructs one publication module, puts it in `all` and
// returns the same instance for boot() to bind.
func TestWireComposesOneGitPublicationModule(t *testing.T) {
	build := gitpublishBootFunc(t, "wire.go", "buildModules")
	var news []*ast.AssignStmt
	inAll, returned := 0, 0
	ast.Inspect(build.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			if len(value.Rhs) == 1 {
				if call, ok := value.Rhs[0].(*ast.CallExpr); ok && communicationBootSelectorPath(call.Fun) == "gitpublish.New" {
					news = append(news, value)
				}
			}
		case *ast.CompositeLit:
			if array, ok := value.Type.(*ast.ArrayType); ok && communicationBootSelectorPath(array.Elt) == "api.Module" {
				for _, element := range value.Elts {
					if communicationBootIdentifier(element, "gpub") {
						inAll++
					}
				}
			}
			if communicationBootIdentifier(value.Type, "moduleSet") {
				for _, element := range value.Elts {
					if kv, ok := element.(*ast.KeyValueExpr); ok && communicationBootIdentifier(kv.Key, "gitpublish") &&
						communicationBootIdentifier(kv.Value, "gpub") {
						returned++
					}
				}
			}
		}
		return true
	})
	if len(news) != 1 || len(news[0].Lhs) != 1 || !communicationBootIdentifier(news[0].Lhs[0], "gpub") {
		t.Fatalf("gitpublish.New constructions = %d, want one assigned to gpub", len(news))
	}
	if inAll != 1 || returned != 1 {
		t.Fatalf("gpub in all = %d, returned as moduleSet.gitpublish = %d; want one each", inAll, returned)
	}
}

type fakeGitpublishSources map[model.ID]model.SourceDef

func (f fakeGitpublishSources) GetByID(_ context.Context, id model.ID) (model.SourceDef, bool, error) {
	def, ok := f[id]
	return def, ok, nil
}

type fakeGitpublishSecrets struct {
	values map[string]string
	reads  []string
}

func (f *fakeGitpublishSecrets) Resolve(_ context.Context, scope model.TenantID, name string) ([]byte, error) {
	f.reads = append(f.reads, scope.String()+"|"+name)
	v, ok := f.values[name]
	if !ok || scope != auth.GlobalSecretScope {
		return nil, auth.ErrSecretNotFound
	}
	return []byte(v), nil
}

type fakeGitpublishInit struct{ paths []string }

func (f *fakeGitpublishInit) InitManaged(_ context.Context, path string) error {
	f.paths = append(f.paths, path)
	return os.Mkdir(path, 0o700)
}

const (
	gitpublishTestTenant    model.TenantID = "tenant-a"
	gitpublishTestWorkspace model.ID       = "ws-1"
)

func gitpublishTestSource(id model.ID, kind string, config map[string]string) model.SourceDef {
	def := model.SourceDef{Scope: auth.GlobalSourceScope, Name: "src-" + string(id), Kind: kind, Tenant: gitpublishTestTenant.String(), Enabled: true, Config: config}
	def.ID, def.Version = id, 3
	return def
}

func newTestGitpublishCustody(t *testing.T, defs ...model.SourceDef) (*gitpublishCustody, *fakeGitpublishSecrets, *fakeGitpublishInit) {
	t.Helper()
	sources := fakeGitpublishSources{}
	for _, d := range defs {
		sources[d.ID] = d
	}
	secrets := &fakeGitpublishSecrets{values: map[string]string{"git-host/acme-app": "-----BEGIN KEY-----", "git-host/acme-bot": "glpat-bot"}}
	inits := &fakeGitpublishInit{}
	return &gitpublishCustody{sources: sources, secrets: secrets, repos: inits, root: t.TempDir(), doer: gp.NewHTTPClient(time.Second)}, secrets, inits
}

func githubPublicationConfig() map[string]string {
	return map[string]string{
		"org": "acme", "app_id": "12", "installation_id": "34",
		gitpublishCredentialKey: "store:git-host/acme-app", gitpublishRepositoriesKey: "acme/widgets, acme/gears",
	}
}

func TestGitpublishCustodySelectsApprovedBindings(t *testing.T) {
	ctx := context.Background()
	c, secrets, inits := newTestGitpublishCustody(t,
		gitpublishTestSource("gh", "github", githubPublicationConfig()),
		gitpublishTestSource("gl", "gitlab", map[string]string{"group": "acme", gitpublishCredentialKey: "store:git-host/acme-bot", gitpublishRepositoriesKey: "acme/tools"}))

	cb, err := c.CredentialBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gh")
	if err != nil || cb.ID != "gh" || cb.Version != 3 || cb.Host != "github" || len(cb.AllowedOwners) != 1 || cb.AllowedOwners[0] != "acme" {
		t.Fatalf("credential binding = %+v %v", cb, err)
	}
	rb, err := c.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gh:acme/widgets")
	if err != nil || rb.ID != "gh:acme/widgets" || rb.Version != 3 || rb.Owner != "acme" || rb.Name != "widgets" || rb.RepoID != "api.github.com/acme/widgets" {
		t.Fatalf("repository binding = %+v %v", rb, err)
	}
	if filepath.Dir(rb.LocalPath) != c.root || !strings.HasSuffix(rb.LocalPath, ".git") {
		t.Fatalf("server repository %q is not engine-owned under %q", rb.LocalPath, c.root)
	}
	if _, err := c.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gh:acme/widgets"); err != nil {
		t.Fatal(err)
	}
	if len(inits.paths) != 1 || inits.paths[0] != rb.LocalPath {
		t.Fatalf("server repositories created = %v, want %s once", inits.paths, rb.LocalPath)
	}
	if len(secrets.reads) != 0 {
		t.Fatalf("binding selection read secrets %v", secrets.reads)
	}

	host, err := c.OpenHost(ctx, gitpublishTestTenant, cb, rb)
	if _, ok := host.(*gp.GitHub); err != nil || !ok {
		t.Fatalf("github host = %T %v", host, err)
	}
	if len(secrets.reads) != 1 || secrets.reads[0] != auth.GlobalSecretScope.String()+"|git-host/acme-app" {
		t.Fatalf("secret reads = %v, want only the deployment-scope git-host credential", secrets.reads)
	}

	glcb, err := c.CredentialBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gl")
	if err != nil {
		t.Fatal(err)
	}
	glrb, err := c.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gl:acme/tools")
	if err != nil || glrb.RepoID != "gitlab.com/acme/tools" {
		t.Fatalf("gitlab repository binding = %+v %v", glrb, err)
	}
	if host, err := c.OpenHost(ctx, gitpublishTestTenant, glcb, glrb); err != nil {
		t.Fatal(err)
	} else if _, ok := host.(*gp.GitLab); !ok {
		t.Fatalf("gitlab host = %T", host)
	}
	// A pair from two rows is refused before any credential is read.
	reads := len(secrets.reads)
	if _, err := c.OpenHost(ctx, gitpublishTestTenant, cb, glrb); !errors.Is(err, gitpublish.ErrBindingNotApproved) || len(secrets.reads) != reads {
		t.Fatalf("mixed pair = %v, reads %d→%d", err, reads, len(secrets.reads))
	}
}

func TestGitpublishCustodyRefusesUnapprovedBindings(t *testing.T) {
	ctx := context.Background()
	with := func(mutate func(d *model.SourceDef)) model.SourceDef {
		d := gitpublishTestSource("gh", "github", githubPublicationConfig())
		mutate(&d)
		return d
	}
	for _, c := range []struct {
		name string
		def  model.SourceDef
		cred string
		repo string
	}{
		{"disabled", with(func(d *model.SourceDef) { d.Enabled = false }), "gh", "gh:acme/widgets"},
		{"plugin", with(func(d *model.SourceDef) { d.Plugin = &model.SourcePluginRef{} }), "gh", "gh:acme/widgets"},
		{"other kind", with(func(d *model.SourceDef) { d.Kind = "vault" }), "gh", "gh:acme/widgets"},
		{"other tenant", with(func(d *model.SourceDef) { d.Tenant = "tenant-b" }), "gh", "gh:acme/widgets"},
		{"tenant-scoped row", with(func(d *model.SourceDef) { d.Scope = gitpublishTestTenant }), "gh", "gh:acme/widgets"},
		{"no publication credential", with(func(d *model.SourceDef) { delete(d.Config, gitpublishCredentialKey) }), "gh", "gh:acme/widgets"},
		{"env credential", with(func(d *model.SourceDef) { d.Config[gitpublishCredentialKey] = "env:GITHUB_KEY" }), "gh", "gh:acme/widgets"},
		{"file credential", with(func(d *model.SourceDef) { d.Config[gitpublishCredentialKey] = "file:/etc/key.pem" }), "gh", "gh:acme/widgets"},
		{"store outside git-host", with(func(d *model.SourceDef) { d.Config[gitpublishCredentialKey] = "store:deploy-key" }), "gh", "gh:acme/widgets"},
		{"store escaping git-host", with(func(d *model.SourceDef) { d.Config[gitpublishCredentialKey] = "store:git-host/../deploy-key" }), "gh", "gh:acme/widgets"},
		{"other workspace", with(func(d *model.SourceDef) { d.Config[gitpublishWorkspacesKey] = "ws-2,ws-3" }), "gh", "gh:acme/widgets"},
		{"repository not listed", with(func(*model.SourceDef) {}), "gh", "gh:acme/secrets"},
		{"repository path escape", with(func(d *model.SourceDef) { d.Config[gitpublishRepositoriesKey] = "acme/../x" }), "gh", "gh:acme/../x"},
		{"repository of another row", with(func(*model.SourceDef) {}), "gh", "gl:acme/widgets"},
		{"enterprise host without allowlist", with(func(d *model.SourceDef) { d.Config["api_base"] = "https://ghe.example.com/api/v3" }), "gh", "gh:acme/widgets"},
	} {
		t.Run(c.name, func(t *testing.T) {
			custody, secrets, inits := newTestGitpublishCustody(t, c.def)
			_, cerr := custody.CredentialBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, c.cred)
			_, rerr := custody.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, c.repo)
			if !errors.Is(rerr, gitpublish.ErrBindingNotApproved) {
				t.Fatalf("repository binding err = %v, want ErrBindingNotApproved", rerr)
			}
			switch c.name {
			case "repository not listed", "repository path escape", "repository of another row", "enterprise host without allowlist":
				if cerr != nil {
					t.Fatalf("credential binding err = %v, want approved", cerr)
				}
			default:
				if !errors.Is(cerr, gitpublish.ErrBindingNotApproved) {
					t.Fatalf("credential binding err = %v, want ErrBindingNotApproved", cerr)
				}
			}
			if len(secrets.reads) != 0 || len(inits.paths) != 0 {
				t.Fatalf("a refused binding read secrets %v or created repositories %v", secrets.reads, inits.paths)
			}
		})
	}
	if _, err := (&gitpublishCustody{sources: fakeGitpublishSources{}}).CredentialBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "a:b"); !errors.Is(err, gitpublish.ErrBindingNotApproved) {
		t.Fatalf("credential id with a colon = %v", err)
	}
}

func TestGitpublishCustodyRefusesAMovedPin(t *testing.T) {
	ctx := context.Background()
	def := gitpublishTestSource("gh", "github", githubPublicationConfig())
	c, secrets, _ := newTestGitpublishCustody(t, def)
	cb, err := c.CredentialBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gh")
	if err != nil {
		t.Fatal(err)
	}
	rb, err := c.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gh:acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	def.Version++
	c.sources.(fakeGitpublishSources)["gh"] = def
	if _, err := c.OpenHost(ctx, gitpublishTestTenant, cb, rb); !errors.Is(err, errGitpublishBindingChanged) {
		t.Fatalf("open after the row changed = %v, want errGitpublishBindingChanged", err)
	}
	// Only the credential pin is stale: the repository was read after the change.
	current, err := c.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gh:acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenHost(ctx, gitpublishTestTenant, cb, current); !errors.Is(err, errGitpublishBindingChanged) {
		t.Fatalf("open with a stale credential pin = %v, want errGitpublishBindingChanged", err)
	}
	if len(secrets.reads) != 0 {
		t.Fatalf("a moved pin read secrets %v", secrets.reads)
	}
}

func TestNewGitPublicationIsDenyClosed(t *testing.T) {
	sources, secrets := fakeGitpublishSources{}, &fakeGitpublishSecrets{}
	defer func(saved func(string) (string, error)) { gitpublishLookPath = saved }(gitpublishLookPath)

	dir := t.TempDir()
	if c, x, err := newGitPublication(dir, sources, secrets, false); err == nil || c != nil || x != nil {
		t.Fatalf("without a sealer = %v %v %v, want refused", c, x, err)
	}
	gitpublishLookPath = func(string) (string, error) { return "git", nil }
	if _, _, err := newGitPublication(dir, sources, secrets, true); err == nil {
		t.Fatal("a relative git path was pinned")
	}
	if _, err := os.Stat(filepath.Join(dir, "gitpublish")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused construction created state: %v", err)
	}

	git := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitpublishLookPath = func(string) (string, error) { return git, nil }
	c, x, err := newGitPublication(dir, sources, secrets, true)
	if err != nil || c == nil || x == nil {
		t.Fatalf("construction = %v %v %v", c, x, err)
	}
	for _, sub := range []string{"home", "repositories"} {
		st, err := os.Stat(filepath.Join(dir, "gitpublish", sub))
		if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
			t.Fatalf("%s = %v %v, want an engine-owned 0700 directory", sub, st, err)
		}
	}
	if c.root != filepath.Join(dir, "gitpublish", "repositories") {
		t.Fatalf("server repository root = %q", c.root)
	}
}

func TestGitpublishSweepInterval(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, c := range []struct {
		raw  string
		want time.Duration
		on   bool
	}{
		{"", defaultGitpublishSweepInterval, true},
		{"30s", 30 * time.Second, true},
		{"not-a-duration", defaultGitpublishSweepInterval, true},
		{"-1s", defaultGitpublishSweepInterval, true},
		{"0", 0, false},
	} {
		if got, on := gitpublishSweepInterval(c.raw, log); got != c.want || on != c.on {
			t.Errorf("interval(%q) = %v %t, want %v %t", c.raw, got, on, c.want, c.on)
		}
	}
}
