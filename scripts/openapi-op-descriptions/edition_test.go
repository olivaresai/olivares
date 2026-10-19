// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"go/build"
	"reflect"
	"strings"
	"testing"
)

// The assembled Business tree carries two things the Community tree never had: edition
// twins (the same method in a `!enterprise` and an `enterprise` file) and a provider
// that embeds a module and mounts more routes under that module's namespace. Both were
// measured red on 2026-10-15 against the assembled Business tree: the twins as
// "registers 1 route(s) that no APIRoutes body reaches", the provider as "could not
// read *businessCircuitBreakerRoutes.APINamespace()".

// routesInPackage reads one module package on its own, which is what the per-module
// route tests in this directory ask for.
func routesInPackage(dir, root string) ([]moduleRoute, error) {
	p, err := loadPackage(dir, root)
	if err != nil || len(p.apiRoutes) == 0 {
		return nil, err
	}
	return p.routes()
}

// withBuildTags sets the tag set the gate selects files with and restores it.
func withBuildTags(t *testing.T, tags ...string) {
	t.Helper()
	old := buildTags
	buildTags = tags
	t.Cleanup(func() { buildTags = old })
}

func routeByKey(t *testing.T, root, key string) moduleRoute {
	t.Helper()
	routes, err := enumerateModuleRoutes(root)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	for _, r := range routes {
		if r.key() == key {
			return r
		}
	}
	t.Fatalf("%s is not in the roster (%d routes)", key, len(routes))
	return moduleRoute{}
}

func writeTwins(t *testing.T, root string) {
	t.Helper()
	if err := writeFixture(root); err != nil {
		t.Fatal(err)
	}
	if err := addEditionTwins(root); err != nil {
		t.Fatal(err)
	}
}

// A file the build excludes is not a registration. Without this the twin that sorts
// last silently replaces the other in the method table and the survivor of the pair is
// reported as unreachable, so the gate could not look at any tree that held both.
func TestEditionTwinsFollowTheBuildConstraint(t *testing.T) {
	for _, c := range []struct {
		name    string
		tags    []string
		handler string
	}{
		{"no tags selects the Community twin", nil, "handleExtraUnavailable"},
		{"enterprise tag selects the Business twin", []string{"enterprise"}, "handleExtra"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeTwins(t, root)
			withBuildTags(t, c.tags...)
			r := routeByKey(t, root, "GET /v1/m/demo/extra")
			if r.handlerName != c.handler {
				t.Errorf("GET /v1/m/demo/extra mounts %q, want %q (the %v twin)", r.handlerName, c.handler, c.tags)
			}
		})
	}
}

// providerFile is the selftest provider behind the Business tag, as the circuit breaker's is.
var providerFile = "//go:build enterprise\n\n" + fmt.Sprintf(providerSource, "*demo.Module")

func writeProvider(t *testing.T, root string, files map[string]string) {
	t.Helper()
	if err := writeFixture(root); err != nil {
		t.Fatal(err)
	}
	if err := writeFiles(root, map[string]string{"go.mod": "module fixture\n\ngo 1.26.9\n"}); err != nil {
		t.Fatal(err)
	}
	if err := writeFiles(root, files); err != nil {
		t.Fatal(err)
	}
}

// The provider's namespace is the namespace of the module it embeds: Go promotes
// APINamespace, and the engine mounts the provider under it.
func TestEmbeddedProviderMountsUnderTheEmbeddedNamespace(t *testing.T) {
	root := t.TempDir()
	writeProvider(t, root, map[string]string{"cmd/provider/provider.go": providerFile})
	withBuildTags(t, "enterprise")
	r := routeByKey(t, root, "GET /v1/m/demo/gadgets/rules")
	if r.ns != "demo" || r.file != "cmd/provider/provider.go" {
		t.Errorf("route = ns %q at %s, want ns demo at cmd/provider/provider.go", r.ns, r.file)
	}
}

// The provider is not part of the Community binary: no tag, no registration, and the
// embedded module's own routes are still enumerated once.
func TestEmbeddedProviderIsAbsentWithoutItsTag(t *testing.T) {
	root := t.TempDir()
	writeProvider(t, root, map[string]string{"cmd/provider/provider.go": providerFile})
	routes, err := enumerateModuleRoutes(root)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(routes) != 3 {
		t.Errorf("%d routes without the enterprise tag, want the 3 of the demo module", len(routes))
	}
}

// What the gate still cannot read stays a refusal that names the type.
func TestUnreadableProviderStaysCannotLook(t *testing.T) {
	const otherModule = "package other\n\nconst Namespace = \"other\"\n\ntype Module struct{}\n\n" +
		"func (m *Module) APINamespace() string { return Namespace }\n\n" +
		"func (m *Module) APIRoutes(reg RouteRegistrar) {\n\treg.Handle(\"GET\", \"/o\", \"o:r\", m.h)\n}\n\n" +
		"// h returns the other thing.\nfunc (m *Module) h(w, r, mc int) {}\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name: "no embedded module and no namespace of its own",
			files: map[string]string{"cmd/provider/provider.go": strings.Replace(
				providerFile, "struct{ *demo.Module }", "struct{}", 1)},
			want: "could not read *providerRoutes.APINamespace()",
		},
		{
			name: "two embedded modules promote two namespaces",
			files: map[string]string{
				"modules/other/other.go": otherModule,
				"cmd/provider/provider.go": strings.Replace(
					strings.Replace(providerFile, "import \"fixture/modules/demo\"",
						"import (\n\t\"fixture/modules/demo\"\n\t\"fixture/modules/other\"\n)", 1),
					"struct{ *demo.Module }", "struct {\n\t*demo.Module\n\t*other.Module\n}", 1),
			},
			want: "could not read *providerRoutes.APINamespace()",
		},
		{
			name: "embedded module outside the walked packages",
			files: map[string]string{"cmd/provider/provider.go": strings.Replace(
				providerFile, "fixture/modules/demo", "fixture/elsewhere/demo", 1)},
			want: "could not read *providerRoutes.APINamespace()",
		},
		{
			name: "a local variable is not the package constant of the same name",
			files: map[string]string{"cmd/provider/provider.go": strings.Replace(
				providerFile, "\th := providerHandlers{}\n", "\th := providerHandlers{}\n\tproviderPath := suffix()\n", 1)},
			want: "the route pattern is computed",
		},
		{
			name: "its own computed namespace is not replaced by the embedded one",
			files: map[string]string{"cmd/provider/provider.go": providerFile +
				"\nfunc (m *providerRoutes) APINamespace() string { return computedNamespace() }\n"},
			want: "could not read *providerRoutes.APINamespace()",
		},
		{
			name: "an import path that only ends like a walked package",
			files: map[string]string{"cmd/provider/provider.go": strings.Replace(
				providerFile, "fixture/modules/demo", "example.com/vendored/modules/demo", 1)},
			want: "could not read *providerRoutes.APINamespace()",
		},
		{
			name: "a constant local to the function shadows the package one",
			files: map[string]string{"cmd/provider/provider.go": strings.Replace(
				providerFile, "\th := providerHandlers{}\n", "\th := providerHandlers{}\n\tconst providerPath = \"/shadow\"\n", 1)},
			want: "the route pattern is computed",
		},
		{
			name: "a computed pattern is still refused",
			files: map[string]string{"cmd/provider/provider.go": strings.Replace(
				providerFile, "providerPath+\"/rules\"", "providerPath+suffix()+\"/rules\"", 1)},
			want: "the route pattern is computed",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeProvider(t, root, c.files)
			withBuildTags(t, "enterprise")
			_, err := enumerateModuleRoutes(root)
			if err == nil {
				t.Fatal("enumeration succeeded; want a CANNOT LOOK refusal")
			}
			if _, ok := err.(cannotLook); !ok || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %T %q, want cannotLook containing %q", err, err, c.want)
			}
		})
	}
}

// The shapes a provider may spell its embedded module and its prefix in.
func TestEmbeddedProviderShapes(t *testing.T) {
	const own = "\nfunc (providerRoutes) APINamespace() string { return \"own\" }\n"
	for _, c := range []struct {
		name   string
		files  map[string]string
		wantNS string
	}{
		{"value embed", map[string]string{"cmd/provider/provider.go": strings.Replace(
			providerFile, "*demo.Module", "demo.Module", 1)}, "demo"},
		{"aliased import", map[string]string{"cmd/provider/provider.go": strings.Replace(strings.Replace(
			providerFile, "import \"fixture/modules/demo\"", "import d \"fixture/modules/demo\"", 1),
			"*demo.Module", "*d.Module", 1)}, "demo"},
		{"same package", map[string]string{
			"cmd/provider/provider.go": strings.Replace(providerFile, "*demo.Module", "*Module", 1),
			"cmd/provider/module.go": "package main\n\ntype Module struct{}\n\n" +
				"func (m *Module) APINamespace() string { return \"local\" }\n",
		}, "local"},
		{"constant in a sibling file", map[string]string{
			"cmd/provider/provider.go": strings.Replace(providerFile,
				"const providerPath = \"/gadgets\"\n", "", 1),
			"cmd/provider/consts.go": "package main\n\nconst providerPath = \"/gadgets\"\n",
		}, "demo"},
		{"its own readable namespace wins", map[string]string{
			"cmd/provider/provider.go": providerFile + own}, "own"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeProvider(t, root, c.files)
			withBuildTags(t, "enterprise")
			r := routeByKey(t, root, "GET /v1/m/"+c.wantNS+"/gadgets/rules")
			if r.ns != c.wantNS {
				t.Errorf("ns = %q, want %q", r.ns, c.wantNS)
			}
		})
	}
}

// The build the documents describe is linux/amd64 without cgo whatever host runs the
// gate: a macOS or cgo-enabled checkout must read the same files.
func TestBuildSelectionIgnoresTheHost(t *testing.T) {
	root := t.TempDir()
	if err := writeFixture(root); err != nil {
		t.Fatal(err)
	}
	if err := swap(root, "modules/demo/demo.go",
		"\treg.Handle(\"GET\", \"/things/{id}\", \"demo:thing:read\", m.handleGetThing)\n",
		"\treg.Handle(\"GET\", \"/things/{id}\", \"demo:thing:read\", m.handleGetThing)\n\tm.mountPlatform(reg)\n\tm.mountCgo(reg)\n"); err != nil {
		t.Fatal(err)
	}
	mount := func(name, path string) string {
		return "package demo\n\nfunc (m *Module) " + name + "(reg RouteRegistrar) {\n" +
			"\treg.Handle(\"GET\", \"" + path + "\", \"demo:thing:read\", m.handleGetThing)\n}\n"
	}
	if err := writeFiles(root, map[string]string{
		"modules/demo/platform_linux.go":  mount("mountPlatform", "/linux"),
		"modules/demo/platform_darwin.go": mount("mountPlatform", "/darwin"),
		"modules/demo/cgo_on.go":          "//go:build cgo\n\n" + mount("mountCgo", "/cgo"),
		"modules/demo/cgo_off.go":         "//go:build !cgo\n\n" + mount("mountCgo", "/nocgo"),
	}); err != nil {
		t.Fatal(err)
	}
	old := build.Default
	build.Default.GOOS, build.Default.GOARCH, build.Default.CgoEnabled = "darwin", "arm64", true
	t.Cleanup(func() { build.Default = old })
	routes, err := enumerateModuleRoutes(root)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, r := range routes {
		have[r.pattern] = true
	}
	for pattern, want := range map[string]bool{"/linux": true, "/darwin": false, "/nocgo": true, "/cgo": false} {
		if have[pattern] != want {
			t.Errorf("route %s present = %t, want %t", pattern, have[pattern], want)
		}
	}
}

func TestSplitTags(t *testing.T) {
	for in, want := range map[string][]string{
		"":                       nil,
		",":                      nil,
		"enterprise":             {"enterprise"},
		"a,b":                    {"a", "b"},
		"a b":                    {"a", "b"},
		"a\tb\n":                 {"a", "b"},
		"enterprise, addon_airs": {"enterprise", "addon_airs"},
	} {
		if got := splitTags(in); len(got)+len(want) > 0 && !reflect.DeepEqual(got, want) {
			t.Errorf("splitTags(%q) = %#v, want %#v", in, got, want)
		}
	}
}
