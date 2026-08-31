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
	"os"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// newOnboardHarness builds a started runtime + a real SourceStore + a real (sealed)
// SecretStore + a resolver wired to the store, and a reconciler whose prepare seam
// yields a fake connector per kind (so PutConnector's live apply never opens a real
// connector). It is the console-onboarding counterpart to newReconcilerHarness.
func newOnboardHarness(t *testing.T) (*sourceReconciler, *auth.SourceStore, *auth.SecretStore) {
	t.Helper()
	ctx := context.Background()
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, rt.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()); _ = st.Close() })

	srcStore := auth.NewSourceStore(st)
	sealer, err := newSecretSealer(t.TempDir(), os.Getenv)
	if err != nil {
		t.Fatalf("secret sealer: %v", err)
	}
	secretStore := auth.NewSecretStore(st, sealer)
	resolver := newSecretResolver(secretStore, os.Getenv, quietLog())
	sr := newSourceReconciler(rt, srcStore, resolver, secretStore, t.TempDir(), nil, quietLog())
	sr.prepare = func(_ context.Context, def model.SourceDef) (*runtime.PreparedSource, sdk.Config, string) {
		return sr.rt.PrepareInProcSource(&recFakeSource{name: "olivares." + def.Kind}), sdk.Config{Settings: def.Config}, ""
	}
	return sr, srcStore, secretStore
}

// TestConnectorCatalogKindsBuild is the drift guard: every kind the console offers
// MUST construct via buildInProcSource. A renamed/removed connector kind fails here
// rather than 404ing silently in the console.
func TestConnectorCatalogKindsBuild(t *testing.T) {
	for _, kind := range inProcConnectorKinds {
		if _, ok := buildInProcSource(kind); !ok {
			t.Errorf("catalog kind %q does not build via buildInProcSource (update inProcConnectorKinds or the switch)", kind)
		}
	}
}

// TestListConnectorsShapesCatalog proves the catalog carries real declared fields for
// an in-process kind and honestly degrades for an out-of-process (plugin) kind.
func TestListConnectorsShapesCatalog(t *testing.T) {
	sr, _, _ := newOnboardHarness(t)
	infos, err := sr.ListConnectors(context.Background())
	if err != nil {
		t.Fatalf("list connectors: %v", err)
	}
	byKind := map[string]api.ConnectorInfo{}
	for _, i := range infos {
		byKind[i.Kind] = i
	}

	// vault is in-process: fields are known and at least one is a Secret field.
	v, ok := byKind["vault"]
	if !ok {
		t.Fatal("catalog missing the in-process 'vault' kind")
	}
	if v.Transport != "in_process" || !v.FieldsKnown {
		t.Fatalf("vault info = %+v, want in_process + fields_known", v)
	}
	hasSecret := false
	for _, f := range v.Fields {
		if f.Secret {
			hasSecret = true
		}
	}
	if !hasSecret {
		t.Errorf("vault should declare at least one Secret field; fields = %+v", v.Fields)
	}

	// claude is out-of-process: the host knows no fields (honest degradation).
	c, ok := byKind["claude"]
	if !ok {
		t.Fatal("catalog missing the out-of-process 'claude' kind")
	}
	if c.Transport != "plugin" || c.FieldsKnown || len(c.Fields) != 0 {
		t.Fatalf("claude info = %+v, want plugin + !fields_known + no fields", c)
	}
}

// TestPutConnectorSealsInlineSecret is the heart of this: an inline credential is
// sealed into the secret store and the durable roster keeps ONLY a reference.
func TestPutConnectorSealsInlineSecret(t *testing.T) {
	sr, srcStore, secretStore := newOnboardHarness(t)
	ctx := context.Background()

	res, err := sr.PutConnector(ctx, recAdmin(), api.ConnectorOnboardInput{
		Name: "vault-prod", Kind: "vault", Tenant: "acme", Enabled: true,
		Config:  map[string]string{"base_url": "https://vault.example:8200"},
		Secrets: map[string]string{"token": "hvs.SUPERSECRETVALUE"},
	})
	if err != nil {
		t.Fatalf("put connector: %v", err)
	}
	if !res.Persisted {
		t.Fatalf("apply result = %+v, want persisted", res)
	}

	def, found, err := srcStore.Get(ctx, auth.GlobalSourceScope, "vault-prod")
	if err != nil || !found {
		t.Fatalf("get source = %v found=%v", err, found)
	}
	const wantRef = "store:source/vault-prod/token"
	if def.Config["token"] != wantRef {
		t.Fatalf("roster token = %q, want the sealed reference %q", def.Config["token"], wantRef)
	}
	if def.Config["base_url"] != "https://vault.example:8200" {
		t.Errorf("non-secret setting not preserved: %v", def.Config)
	}
	// The durable roster MUST NOT carry the literal credential anywhere.
	for k, v := range def.Config {
		if v == "hvs.SUPERSECRETVALUE" {
			t.Fatalf("roster leaked the literal credential in field %q", k)
		}
	}
	// The sealed value is in the secret store (a non-secret hint, the real value on Resolve).
	view, ok, err := secretStore.Get(ctx, auth.GlobalSecretScope, "source/vault-prod/token")
	if err != nil || !ok {
		t.Fatalf("owned secret get = %v ok=%v", err, ok)
	}
	if view.Hint == "" {
		t.Error("owned secret should carry a non-secret hint")
	}
	got, err := secretStore.Resolve(ctx, auth.GlobalSecretScope, "source/vault-prod/token")
	if err != nil || string(got) != "hvs.SUPERSECRETVALUE" {
		t.Fatalf("owned secret resolve = %q,%v, want the sealed value", string(got), err)
	}
}

// TestPutConnectorBlankKeepsAndReferencePassthrough covers the two non-sealing
// branches: a blank secret field keeps the stored reference, and a value that is
// already a reference is used verbatim (reuse an existing/external secret).
func TestPutConnectorBlankKeepsAndReferencePassthrough(t *testing.T) {
	sr, srcStore, _ := newOnboardHarness(t)
	ctx := context.Background()

	// 1) Seal an inline secret.
	if _, err := sr.PutConnector(ctx, recAdmin(), api.ConnectorOnboardInput{
		Name: "vault-prod", Kind: "vault", Tenant: "acme", Enabled: true,
		Config:  map[string]string{"base_url": "https://a:8200"},
		Secrets: map[string]string{"token": "hvs.ORIGINAL"},
	}); err != nil {
		t.Fatalf("initial put: %v", err)
	}

	// 2) Edit with a BLANK secret (and a changed non-secret setting): the token
	// reference is preserved (blank = keep the stored sealed value).
	if _, err := sr.PutConnector(ctx, recAdmin(), api.ConnectorOnboardInput{
		Name: "vault-prod", Kind: "vault", Tenant: "acme", Enabled: true,
		Config:  map[string]string{"base_url": "https://b:8200"},
		Secrets: map[string]string{"token": ""},
	}); err != nil {
		t.Fatalf("blank-keep put: %v", err)
	}
	def, _, _ := srcStore.Get(ctx, auth.GlobalSourceScope, "vault-prod")
	if def.Config["token"] != "store:source/vault-prod/token" {
		t.Fatalf("blank edit lost the token reference: %v", def.Config)
	}
	if def.Config["base_url"] != "https://b:8200" {
		t.Errorf("blank edit did not update the non-secret setting: %v", def.Config)
	}

	// 3) Point the field at an existing/external reference: stored verbatim, NOT sealed.
	if _, err := sr.PutConnector(ctx, recAdmin(), api.ConnectorOnboardInput{
		Name: "vault-prod", Kind: "vault", Tenant: "acme", Enabled: true,
		Config:  map[string]string{"base_url": "https://b:8200"},
		Secrets: map[string]string{"token": "vault:secret/data/prod#token"},
	}); err != nil {
		t.Fatalf("reference put: %v", err)
	}
	def, _, _ = srcStore.Get(ctx, auth.GlobalSourceScope, "vault-prod")
	if def.Config["token"] != "vault:secret/data/prod#token" {
		t.Fatalf("reference field not stored verbatim: %v", def.Config)
	}
}

// TestDeleteConnectorCascadesOwnedSecretOnly proves a delete removes the
// onboarding-OWNED sealed credential but leaves an operator-supplied reference's
// target untouched.
func TestDeleteConnectorCascadesOwnedSecretOnly(t *testing.T) {
	sr, srcStore, secretStore := newOnboardHarness(t)
	ctx := context.Background()

	// A standalone, operator-managed secret the connector merely references.
	if _, err := secretStore.Put(ctx, recAdmin(), auth.GlobalSecretScope, "shared-cred", "shared-value", "operator-owned"); err != nil {
		t.Fatalf("seed shared secret: %v", err)
	}

	// A connector with an inline (owned) token AND a non-secret field referencing the
	// shared secret.
	if _, err := sr.PutConnector(ctx, recAdmin(), api.ConnectorOnboardInput{
		Name: "vault-a", Kind: "vault", Tenant: "acme", Enabled: true,
		Config:  map[string]string{"base_url": "https://a:8200", "extra_ref": "store:shared-cred"},
		Secrets: map[string]string{"token": "hvs.OWNED"},
	}); err != nil {
		t.Fatalf("put connector: %v", err)
	}

	res, err := sr.DeleteConnector(ctx, recAdmin(), "vault-a")
	if err != nil {
		t.Fatalf("delete connector: %v", err)
	}
	if !res.Persisted || res.Action != "removed" {
		t.Fatalf("delete result = %+v", res)
	}
	if _, found, _ := srcStore.Get(ctx, auth.GlobalSourceScope, "vault-a"); found {
		t.Fatal("source still present after delete")
	}
	// The owned credential is gone.
	if _, ok, _ := secretStore.Get(ctx, auth.GlobalSecretScope, "source/vault-a/token"); ok {
		t.Error("onboarding-owned secret should be cascade-deleted")
	}
	// The operator-managed shared secret is untouched.
	if got, err := secretStore.Resolve(ctx, auth.GlobalSecretScope, "shared-cred"); err != nil || string(got) != "shared-value" {
		t.Errorf("operator-managed shared secret must survive: %q %v", string(got), err)
	}
}

// TestTestConnectorRejectsUnsupportedKinds proves the connectivity test is honest
// about what it cannot probe on the host.
func TestTestConnectorRejectsUnsupportedKinds(t *testing.T) {
	sr, _, _ := newOnboardHarness(t)
	ctx := context.Background()

	if err := sr.TestConnector(ctx, recAdmin(), api.ConnectorOnboardInput{Name: "x", Kind: "no-such-kind", Tenant: "acme"}); !errors.Is(err, auth.ErrBadSourceDef) {
		t.Errorf("unknown kind test = %v, want ErrBadSourceDef", err)
	}
	// A plugin (out-of-process) kind cannot be opened on the host; honest refusal.
	if err := sr.TestConnector(ctx, recAdmin(), api.ConnectorOnboardInput{Name: "x", Kind: "claude", Tenant: "acme"}); !errors.Is(err, auth.ErrBadSourceDef) {
		t.Errorf("plugin kind test = %v, want ErrBadSourceDef (validated on save)", err)
	}
}

// TestResolveTestConfig proves the test-config builder: a blank secret field opens
// the EXISTING sealed value via its stored reference, and an inline literal is
// overlaid AFTER resolution (so the resolver's strict mode never sees it).
func TestResolveTestConfig(t *testing.T) {
	sr, srcStore, secretStore := newOnboardHarness(t)
	ctx := context.Background()

	// An existing source whose token references a sealed secret = "STORED".
	if _, err := secretStore.Put(ctx, recAdmin(), auth.GlobalSecretScope, "source/vp/token", "STORED", ""); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := srcStore.Put(ctx, recAdmin(), model.SourceDef{
		Scope: auth.GlobalSourceScope, Name: "vp", Kind: "vault", Tenant: "acme", Enabled: true,
		Config: map[string]string{"token": "store:source/vp/token"},
	}); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	desc := sdk.Descriptor{ConfigFields: []sdk.ConfigField{{Key: "token", Secret: true}, {Key: "base_url"}}}

	// Blank token → opens the stored sealed value.
	resolved, err := sr.resolveTestConfig(ctx, desc, api.ConnectorOnboardInput{
		Name: "vp", Config: map[string]string{"base_url": "https://x"}, Secrets: map[string]string{"token": ""},
	})
	if err != nil {
		t.Fatalf("resolve (blank): %v", err)
	}
	if resolved.Settings["token"] != "STORED" {
		t.Fatalf("blank token resolved to %q, want the opened stored value", resolved.Settings["token"])
	}

	// Inline literal → overlaid verbatim (the resolver never rejected it).
	resolved, err = sr.resolveTestConfig(ctx, desc, api.ConnectorOnboardInput{
		Name: "vp", Secrets: map[string]string{"token": "TYPED-NOW"},
	})
	if err != nil {
		t.Fatalf("resolve (literal): %v", err)
	}
	if resolved.Settings["token"] != "TYPED-NOW" {
		t.Fatalf("inline token resolved to %q, want the typed literal", resolved.Settings["token"])
	}
}

// switchCaseKinds parses a composition-root file and returns every case label
// (kind string) of the named builder function's switch. Reading the source is
// deliberate: Go cannot enumerate a switch at runtime, and a maintained shadow list
// would itself drift — the AST is the ground truth the catalogs must cover.
func switchCaseKinds(t *testing.T, filename, funcName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	var kinds []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != funcName {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range cc.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				kind, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					t.Fatalf("unquote case label %s: %v", lit.Value, uerr)
				}
				kinds = append(kinds, kind)
			}
			return true
		})
	}
	if len(kinds) == 0 {
		t.Fatalf("found no case kinds in %s (did the function move out of %s?)", funcName, filename)
	}
	return kinds
}

// buildInProcSourceCaseKinds returns the case labels of the buildInProcSource switch.
func buildInProcSourceCaseKinds(t *testing.T) []string {
	t.Helper()
	return switchCaseKinds(t, "sources.go", "buildInProcSource")
}

// TestConnectorCatalogCoversSwitch is the INVERSE drift guard (E4a): every kind
// buildInProcSource can construct MUST be offered in the console catalog
// (inProcConnectorKinds) unless it is deliberately excluded below. Without this, a
// newly wired connector silently never gets a console card (17 kinds had drifted
// that way by 2026-07).
func TestConnectorCatalogCoversSwitch(t *testing.T) {
	// Deliberate exclusions, each with its reason. Aliases resolve to the SAME
	// connector as their canonical kind, which IS in the catalog — offering both
	// would present one connector as two.
	excluded := map[string]string{
		"okta":          "alias of idp (same connector, seeded provider); catalog offers the canonical idp",
		"entra":         "alias of idp (same connector, seeded provider); catalog offers the canonical idp",
		"pg-audit":      "alias of pgaudit; catalog offers the canonical pgaudit",
		"s3-cloudtrail": "alias of s3cloudtrail; catalog offers the canonical s3cloudtrail",
	}
	offered := map[string]bool{}
	for _, k := range inProcConnectorKinds {
		offered[k] = true
	}
	for _, kind := range buildInProcSourceCaseKinds(t) {
		if offered[kind] {
			continue
		}
		if _, ok := excluded[kind]; ok {
			continue
		}
		t.Errorf("kind %q is wired in buildInProcSource but not offered in inProcConnectorKinds (add it to the catalog, or add a reasoned exclusion)", kind)
	}
	// The exclusion list must not rot: every excluded kind must still exist in the
	// switch, and must not ALSO be in the catalog.
	inSwitch := map[string]bool{}
	for _, k := range buildInProcSourceCaseKinds(t) {
		inSwitch[k] = true
	}
	for kind, why := range excluded {
		if !inSwitch[kind] {
			t.Errorf("excluded kind %q (%s) is no longer in buildInProcSource; drop it from the exclusion list", kind, why)
		}
		if offered[kind] {
			t.Errorf("kind %q is both excluded and offered; resolve the contradiction", kind)
		}
	}
}

// --- hosting, the self-hosted-vs-vendor answer an operator reads at a glance ---

// TestHostingFromFields drives the DERIVATION directly, including the two directions a
// "local is self-hosted" assertion alone cannot distinguish: a rule that answered
// self_hosted for everything would pass that assertion, and so would a rule that only
// ever looked at the FIRST field.
func TestHostingFromFields(t *testing.T) {
	f := func(key, def string) sdk.ConfigField {
		return sdk.ConfigField{Key: key, Type: sdk.FieldString, Default: def}
	}
	cases := []struct {
		name   string
		fields []sdk.ConfigField
		want   string
	}{
		{"loopback name", []sdk.ConfigField{f("u", "http://localhost:11434")}, api.HostingSelfHosted},
		{"loopback ipv4", []sdk.ConfigField{f("u", "https://127.0.0.1:8200")}, api.HostingSelfHosted},
		{"loopback ipv4 not dot one", []sdk.ConfigField{f("u", "http://127.9.9.9:8080")}, api.HostingSelfHosted},
		{"loopback ipv6", []sdk.ConfigField{f("u", "http://[::1]:8080")}, api.HostingSelfHosted},
		// The operator's OWN NETWORK is not a vendor cloud. Before the contrast these
		// four were classified vendor_hosted, which is the one answer nobody is at.
		{"rfc1918 ten", []sdk.ConfigField{f("u", "http://10.0.0.5:8080")}, api.HostingSelfHosted},
		{"rfc1918 192.168", []sdk.ConfigField{f("u", "https://192.168.1.10")}, api.HostingSelfHosted},
		{"link-local", []sdk.ConfigField{f("u", "http://169.254.169.254")}, api.HostingSelfHosted},
		{"cgnat / overlay", []sdk.ConfigField{f("u", "http://100.101.102.103:11434")}, api.HostingSelfHosted},
		{"ipv6 ULA", []sdk.ConfigField{f("u", "http://[fd00::1]:8200")}, api.HostingSelfHosted},
		{"unspecified", []sdk.ConfigField{f("u", "http://0.0.0.0:8080")}, api.HostingSelfHosted},
		// …and a PUBLIC address still is one: without this the rule could answer
		// self_hosted for every IP literal and the cases above would prove nothing.
		{"public ipv4 literal", []sdk.ConfigField{f("u", "https://8.8.8.8")}, api.HostingVendorHosted},
		{"public ipv6 literal", []sdk.ConfigField{f("u", "https://[2606:4700::1111]")}, api.HostingVendorHosted},
		{"vendor url", []sdk.ConfigField{f("u", "https://api.openai.com")}, api.HostingVendorHosted},
		// ORDER must not decide: vendor first, loopback second, still self-hosted.
		{"vendor then loopback", []sdk.ConfigField{
			f("a", "https://api.example.com"), f("b", "http://localhost:9000"),
		}, api.HostingSelfHosted},
		{"loopback then vendor", []sdk.ConfigField{
			f("a", "http://localhost:9000"), f("b", "https://api.example.com"),
		}, api.HostingSelfHosted},
		// The NON-FIRING direction: nothing that parses as an http(s) URL ⇒ unknown.
		{"no defaults at all", []sdk.ConfigField{{Key: "token", Type: sdk.FieldString, Secret: true}}, api.HostingUnknown},
		{"non-url default", []sdk.ConfigField{f("region", "eu-west-1")}, api.HostingUnknown},
		{"path default", []sdk.ConfigField{f("dir", "/etc/olivares")}, api.HostingUnknown},
		{"non-http scheme", []sdk.ConfigField{f("u", "postgres://localhost:5432/db")}, api.HostingUnknown},
		{"empty field list", nil, api.HostingUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hostingFromFields(c.fields); got != c.want {
				t.Errorf("hostingFromFields = %q, want %q", got, c.want)
			}
		})
	}
}

// TestListConnectorsHostingIsDerivedFromTheRealCatalog pins the answer for the kinds an
// operator actually confuses, against the REAL descriptors this build composes — not a
// fixture. gemini / gemini-cli / vertex are THREE different things and are asserted as
// three, so a change that collapsed them would fail here.
func TestListConnectorsHostingIsDerivedFromTheRealCatalog(t *testing.T) {
	sr, _, _ := newOnboardHarness(t)
	infos, err := sr.ListConnectors(context.Background())
	if err != nil {
		t.Fatalf("list connectors: %v", err)
	}
	byKind := map[string]api.ConnectorInfo{}
	for _, i := range infos {
		byKind[i.Kind] = i
	}

	want := map[string]string{
		// The three base providers exists to make visible.
		"local":  api.HostingSelfHosted,
		"openai": api.HostingVendorHosted,
		"gemini": api.HostingVendorHosted,
		// The two kinds an operator mixes up with gemini. Distinct rows, distinct answers.
		"gemini-cli": api.HostingUnknown,      // a local settings.json observer: no endpoint
		"vertex":     api.HostingVendorHosted, // Google's cloud
		// The other measured loopback kinds, so the rule cannot quietly narrow to `local`.
		"vault":       api.HostingSelfHosted,
		"vault-audit": api.HostingSelfHosted,
	}
	for kind, wantHosting := range want {
		info, ok := byKind[kind]
		if !ok {
			t.Errorf("catalog missing kind %q", kind)
			continue
		}
		if info.Hosting != wantHosting {
			t.Errorf("kind %q hosting = %q, want %q", kind, info.Hosting, wantHosting)
		}
	}

	// Every kind carries one of the three answers — an empty string would render as a
	// missing badge that looks like "vendor" to a reader.
	for _, i := range infos {
		switch i.Hosting {
		case api.HostingSelfHosted, api.HostingVendorHosted, api.HostingUnknown:
		default:
			t.Errorf("kind %q has hosting %q, outside the vocabulary", i.Kind, i.Hosting)
		}
		// A plugin's fields are not host-known, so it can only honestly be unknown.
		if i.Transport == "plugin" && i.Hosting != api.HostingUnknown {
			t.Errorf("plugin kind %q claims hosting %q; the host cannot introspect it", i.Kind, i.Hosting)
		}
	}

	// The DISTRIBUTION, measured against the live catalog on 2026-08-09. This is what
	// makes the assertions above more than three lucky rows: a rule that answered
	// self_hosted (or vendor_hosted) too eagerly moves these counts even when local,
	// openai and gemini stay right.
	counts := map[string]int{}
	for _, i := range infos {
		counts[i.Hosting]++
	}
	if counts[api.HostingSelfHosted] != 3 {
		t.Errorf("self_hosted count = %d, want 3 (local, vault, vault-audit). "+
			"A new loopback-defaulting connector is a REAL change: re-measure and update this number.",
			counts[api.HostingSelfHosted])
	}
	if counts[api.HostingVendorHosted] == 0 || counts[api.HostingUnknown] == 0 {
		t.Errorf("degenerate distribution self=%d vendor=%d unknown=%d: a classifier that answers "+
			"the same thing for every input has not classified anything",
			counts[api.HostingSelfHosted], counts[api.HostingVendorHosted], counts[api.HostingUnknown])
	}
}

// TestHostingKnownCounterexample records, as an executable statement, the ONE kind in
// this build whose hosting answer does not mean what the badge implies.
//
// `mcp` declares three auxiliary public feeds (registry, deprecation, Docker catalog),
// all opt-in, while the servers it actually introspects come from config and may be
// local stdio commands. The derivation reads declared default endpoints, so it says
// vendor_hosted. That is a real limitation, and this test exists so it stays VISIBLE:
// if a later change makes `mcp` unknown (or gives the SDK the semantic metadata that
// would fix it properly), this fails and the comment gets corrected with it, instead of
// the limitation quietly outliving its own documentation.
func TestHostingKnownCounterexample(t *testing.T) {
	sr, _, _ := newOnboardHarness(t)
	infos, err := sr.ListConnectors(context.Background())
	if err != nil {
		t.Fatalf("list connectors: %v", err)
	}
	for _, i := range infos {
		if i.Kind != "mcp" {
			continue
		}
		if i.Hosting != api.HostingVendorHosted {
			t.Fatalf("mcp hosting = %q, want %q. If this changed on purpose, update the "+
				"KNOWN COUNTEREXAMPLE note on hostingFromFields in the same commit.",
				i.Hosting, api.HostingVendorHosted)
		}
		return
	}
	t.Fatal("catalog no longer offers the 'mcp' kind; the counterexample note is stale")
}
