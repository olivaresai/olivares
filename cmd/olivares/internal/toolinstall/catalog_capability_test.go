// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestCapabilityCatalogTypedLookups(t *testing.T) {
	claude := &recordingProviderV1{key: claudeDriver}
	codex := &recordingProviderV2{key: DriverCodex}
	cat, err := NewCapabilityCatalog(NewCatalog(claude), codex)
	if err != nil {
		t.Fatalf("NewCapabilityCatalog: %v", err)
	}
	if got := cat.Keys(); len(got) != 2 || got[0] != claudeDriver || got[1] != DriverCodex {
		t.Fatalf("Keys()=%v, want sorted [%s %s]", got, claudeDriver, DriverCodex)
	}

	capClaude, err := cat.Capability(claudeDriver)
	if err != nil || capClaude != ProviderCapabilityV1 {
		t.Fatalf("Capability(claude)=%v, %v", capClaude, err)
	}
	capCodex, err := cat.Capability(DriverCodex)
	if err != nil || capCodex != ProviderCapabilityV2 {
		t.Fatalf("Capability(codex)=%v, %v", capCodex, err)
	}

	gotV1, err := cat.LookupV1(claudeDriver)
	if err != nil {
		t.Fatalf("LookupV1(claude): %v", err)
	}
	if gotV1 != claude {
		t.Fatalf("LookupV1(claude) returned a different Provider")
	}
	gotV2, err := cat.LookupV2(DriverCodex)
	if err != nil {
		t.Fatalf("LookupV2(codex): %v", err)
	}
	if gotV2 != codex {
		t.Fatalf("LookupV2(codex) returned a different PackageProviderV2")
	}
	if claude.resolves != 0 || codex.resolves != 0 {
		t.Fatalf("lookups invoked Resolve/ResolveV2: v1=%d v2=%d", claude.resolves, codex.resolves)
	}

	nilV1, err := NewCapabilityCatalog(nil, codex)
	if err != nil {
		t.Fatalf("nil v1: %v", err)
	}
	if got := nilV1.Keys(); len(got) != 1 || got[0] != DriverCodex {
		t.Fatalf("nil v1 Keys()=%v, want [%s]", got, DriverCodex)
	}
}

func TestCapabilityCatalogRefusesDuplicateAndCollision(t *testing.T) {
	claude := &recordingProviderV1{key: claudeDriver}
	codex := &recordingProviderV2{key: DriverCodex}
	dup := &recordingProviderV2{key: DriverCodex}
	_, err := NewCapabilityCatalog(NewCatalog(claude), codex, dup)
	if KindOf(err) != KindInvalidRequest {
		t.Fatalf("duplicate v2: %v", err)
	}

	clash := &recordingProviderV2{key: claudeDriver}
	_, err = NewCapabilityCatalog(NewCatalog(claude), clash)
	if KindOf(err) != KindInvalidRequest {
		t.Fatalf("v1/v2 collision: %v", err)
	}

	_, err = NewCapabilityCatalog(NewCatalog(claude), nil)
	if KindOf(err) != KindInvalidRequest {
		t.Fatalf("nil v2 provider: %v", err)
	}

	_, err = NewCapabilityCatalog(NewCatalog(claude), &recordingProviderV2{key: ""})
	if KindOf(err) != KindInvalidRequest {
		t.Fatalf("empty v2 key: %v", err)
	}

	_, err = NewCapabilityCatalog(&Catalog{byKey: map[string]Provider{"claude": nil}})
	if KindOf(err) != KindInvalidRequest {
		t.Fatalf("nil v1 provider: %v", err)
	}
}

func TestProviderCapabilityAdmission(t *testing.T) {
	claude := &recordingProviderV1{key: claudeDriver}
	codex := &recordingProviderV2{key: DriverCodex}
	cat, err := NewCapabilityCatalog(NewCatalog(claude), codex)
	if err != nil {
		t.Fatalf("NewCapabilityCatalog: %v", err)
	}

	_, err = cat.LookupV1(DriverCodex)
	if KindOf(err) != KindInvalidRequest {
		t.Fatalf("LookupV1(codex): %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), PlanSchemaV2) || !strings.Contains(err.Error(), PlanSchema) {
		t.Fatalf("LookupV1(codex) must name registered and expected schemas: %v", err)
	}

	_, err = cat.LookupV2(claudeDriver)
	if KindOf(err) != KindInvalidRequest {
		t.Fatalf("LookupV2(claude): %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), PlanSchema) || !strings.Contains(err.Error(), PlanSchemaV2) {
		t.Fatalf("LookupV2(claude) must name registered and expected schemas: %v", err)
	}

	_, err = cat.LookupV1("grok")
	if KindOf(err) != KindUnsupportedProvider {
		t.Fatalf("LookupV1(grok): %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), claudeDriver) || !strings.Contains(err.Error(), DriverCodex) {
		t.Fatalf("missing driver must enumerate the union: %v", err)
	}
	_, err = cat.LookupV2("missing")
	if KindOf(err) != KindUnsupportedProvider {
		t.Fatalf("LookupV2(missing): %v", err)
	}
	_, err = cat.Capability("missing")
	if KindOf(err) != KindUnsupportedProvider {
		t.Fatalf("Capability(missing): %v", err)
	}
	if claude.resolves != 0 || codex.resolves != 0 {
		t.Fatalf("admission invoked Resolve/ResolveV2: v1=%d v2=%d", claude.resolves, codex.resolves)
	}
}

func TestCapabilityCatalogRefusesTypedNilProviders(t *testing.T) {
	t.Run("constructor v2", func(t *testing.T) {
		typedNilV2KeyCalls = 0
		var provider *typedNilV2
		_, err := NewCapabilityCatalog(nil, provider)
		if typedNilV2KeyCalls != 0 {
			t.Errorf("typed nil v2 Key calls=%d, want zero", typedNilV2KeyCalls)
		}
		if KindOf(err) != KindInvalidRequest {
			t.Errorf("typed nil v2: kind=%q err=%v, want invalid_request", KindOf(err), err)
		}
	})

	t.Run("constructor v1", func(t *testing.T) {
		typedNilV1KeyCalls = 0
		var provider *typedNilV1
		v1 := &Catalog{byKey: map[string]Provider{"typed-nil-v1": provider}}
		_, err := NewCapabilityCatalog(v1)
		if typedNilV1KeyCalls != 0 {
			t.Errorf("typed nil v1 Key calls=%d, want zero", typedNilV1KeyCalls)
		}
		if KindOf(err) != KindInvalidRequest {
			t.Errorf("typed nil v1: kind=%q err=%v, want invalid_request", KindOf(err), err)
		}
	})

	t.Run("live concrete providers", func(t *testing.T) {
		claude := NewClaude(ClaudeOptions{})
		codex := &recordingProviderV2{key: DriverCodex}
		cat, err := NewCapabilityCatalog(NewCatalog(claude), codex)
		if err != nil {
			t.Fatalf("NewCapabilityCatalog: %v", err)
		}
		gotV1, err := cat.LookupV1(claudeDriver)
		if err != nil {
			t.Fatalf("LookupV1(claude): %v", err)
		}
		if gotV1 != claude {
			t.Fatalf("LookupV1(claude) did not return the live Claude provider")
		}
		if _, ok := gotV1.(*Claude); !ok {
			t.Fatalf("LookupV1(claude) type %T, want *Claude", gotV1)
		}
		gotV2, err := cat.LookupV2(DriverCodex)
		if err != nil {
			t.Fatalf("LookupV2(codex): %v", err)
		}
		if gotV2 != codex {
			t.Fatalf("LookupV2(codex) did not return the concrete v2 provider")
		}
		if codex.resolves != 0 {
			t.Fatalf("positive lookup invoked ResolveV2: %d", codex.resolves)
		}
	})

	t.Run("lookup presence typed nil", func(t *testing.T) {
		claude := NewClaude(ClaudeOptions{})
		codex := &recordingProviderV2{key: DriverCodex}
		cat, err := NewCapabilityCatalog(NewCatalog(claude), codex)
		if err != nil {
			t.Fatalf("NewCapabilityCatalog: %v", err)
		}
		typedNilV1KeyCalls = 0
		typedNilV2KeyCalls = 0
		var nilV1 *typedNilV1
		var nilV2 *typedNilV2
		cat.v1.byKey["typed-nil-v1"] = nilV1
		cat.v2ByKey["typed-nil-v2"] = nilV2

		_, err = cat.Capability("typed-nil-v1")
		if KindOf(err) != KindInvalidRequest {
			t.Errorf("Capability(typed-nil-v1): kind=%q err=%v, want invalid_request", KindOf(err), err)
		}
		_, err = cat.LookupV1("typed-nil-v1")
		if KindOf(err) != KindInvalidRequest {
			t.Errorf("LookupV1(typed-nil-v1): kind=%q err=%v, want invalid_request", KindOf(err), err)
		}
		_, err = cat.Capability("typed-nil-v2")
		if KindOf(err) != KindInvalidRequest {
			t.Errorf("Capability(typed-nil-v2): kind=%q err=%v, want invalid_request", KindOf(err), err)
		}
		_, err = cat.LookupV2("typed-nil-v2")
		if KindOf(err) != KindInvalidRequest {
			t.Errorf("LookupV2(typed-nil-v2): kind=%q err=%v, want invalid_request", KindOf(err), err)
		}
		if typedNilV1KeyCalls != 0 || typedNilV2KeyCalls != 0 {
			t.Errorf("lookup of typed nil called Key: v1=%d v2=%d", typedNilV1KeyCalls, typedNilV2KeyCalls)
		}
		if codex.resolves != 0 {
			t.Errorf("typed-nil lookup invoked ResolveV2: %d", codex.resolves)
		}

		gotV1, err := cat.LookupV1(claudeDriver)
		if err != nil || gotV1 != claude {
			t.Errorf("legal Claude lookup after planting typed nil: got=%v err=%v", gotV1, err)
		}
		gotV2, err := cat.LookupV2(DriverCodex)
		if err != nil || gotV2 != codex {
			t.Errorf("legal v2 lookup after planting typed nil: got=%v err=%v", gotV2, err)
		}
		_, err = cat.LookupV1("missing")
		if KindOf(err) != KindUnsupportedProvider {
			t.Errorf("missing driver: kind=%q err=%v, want unsupported_provider", KindOf(err), err)
		}
		if codex.resolves != 0 {
			t.Errorf("post-plant lookups invoked ResolveV2: %d", codex.resolves)
		}
	})
}

func TestCapabilityCatalogV2DoublesPanicOnRuntimeMethods(t *testing.T) {
	p := &recordingProviderV2{key: DriverCodex}
	requirePanicUnexpected(t, func() { _, _ = p.FetchV2(context.Background(), nil, io.Discard) })
	requirePanicUnexpected(t, func() { _, _ = p.PlaceV2(context.Background(), nil, nil, "", "") })
	requirePanicUnexpected(t, func() { _ = p.VerifyPayload(context.Background(), nil, nil, PackagePolicyV2{}) })
	requirePanicUnexpected(t, func() { _, _ = p.Probe(context.Background(), "", "", "") })
}

func requirePanicUnexpected(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		got := recover()
		if got == nil {
			t.Fatal("want panic(\"unexpected\")")
		}
		if got != "unexpected" {
			t.Fatalf("panic %v, want unexpected", got)
		}
	}()
	fn()
}

type recordingProviderV1 struct {
	key      string
	resolves int
}

func (p *recordingProviderV1) Key() string { return p.key }

func (p *recordingProviderV1) Resolve(context.Context, Request) (*Plan, *Material, error) {
	p.resolves++
	return nil, nil, refuse(KindInvalidRequest, "test double Resolve is not a product adapter")
}

func (p *recordingProviderV1) VerifyMaterial(context.Context, *Material) (*VerifiedManifest, error) {
	panic("unexpected")
}

func (p *recordingProviderV1) FetchArtifact(context.Context, *Plan, io.Writer) (Artifact, error) {
	panic("unexpected")
}

func (p *recordingProviderV1) Probe(context.Context, string, string, string) (ProbeReport, error) {
	panic("unexpected")
}

func (p *recordingProviderV1) DefaultPaths(string) []string { return nil }

type recordingProviderV2 struct {
	key      string
	resolves int
}

func (p *recordingProviderV2) Key() string { return p.key }

func (p *recordingProviderV2) ResolveV2(context.Context, RequestV2) (*PlanV2, *ResolvedMaterialV2, error) {
	p.resolves++
	return nil, nil, refuse(KindInvalidRequest, "test double ResolveV2 is not a product adapter")
}

func (p *recordingProviderV2) FetchV2(context.Context, *PlanV2, io.Writer) (FetchedObjectObserved, error) {
	panic("unexpected")
}

func (p *recordingProviderV2) PlaceV2(context.Context, *PlanV2, *os.Root, string, string) (*ObservedPayloadInventory, error) {
	panic("unexpected")
}

func (p *recordingProviderV2) VerifyPayload(context.Context, PayloadAccess, *ObservedPayloadInventory, PackagePolicyV2) error {
	panic("unexpected")
}

func (p *recordingProviderV2) Probe(context.Context, string, string, string) (ProbeReport, error) {
	panic("unexpected")
}

func (p *recordingProviderV2) DefaultPaths(string) []string { return nil }

var (
	_ Provider          = (*recordingProviderV1)(nil)
	_ PackageProviderV2 = (*recordingProviderV2)(nil)
	_ Provider          = (*typedNilV1)(nil)
	_ PackageProviderV2 = (*typedNilV2)(nil)
)

type typedNilV1 struct{}

var typedNilV1KeyCalls int

func (*typedNilV1) Key() string {
	typedNilV1KeyCalls++
	return "typed-nil-v1"
}
func (*typedNilV1) Resolve(context.Context, Request) (*Plan, *Material, error) {
	panic("unexpected")
}
func (*typedNilV1) VerifyMaterial(context.Context, *Material) (*VerifiedManifest, error) {
	panic("unexpected")
}
func (*typedNilV1) FetchArtifact(context.Context, *Plan, io.Writer) (Artifact, error) {
	panic("unexpected")
}
func (*typedNilV1) Probe(context.Context, string, string, string) (ProbeReport, error) {
	panic("unexpected")
}
func (*typedNilV1) DefaultPaths(string) []string { panic("unexpected") }

type typedNilV2 struct{}

var typedNilV2KeyCalls int

func (*typedNilV2) Key() string {
	typedNilV2KeyCalls++
	return "typed-nil-v2"
}
func (*typedNilV2) ResolveV2(context.Context, RequestV2) (*PlanV2, *ResolvedMaterialV2, error) {
	panic("unexpected")
}
func (*typedNilV2) FetchV2(context.Context, *PlanV2, io.Writer) (FetchedObjectObserved, error) {
	panic("unexpected")
}
func (*typedNilV2) PlaceV2(context.Context, *PlanV2, *os.Root, string, string) (*ObservedPayloadInventory, error) {
	panic("unexpected")
}
func (*typedNilV2) VerifyPayload(context.Context, PayloadAccess, *ObservedPayloadInventory, PackagePolicyV2) error {
	panic("unexpected")
}
func (*typedNilV2) Probe(context.Context, string, string, string) (ProbeReport, error) {
	panic("unexpected")
}
func (*typedNilV2) DefaultPaths(string) []string { panic("unexpected") }
