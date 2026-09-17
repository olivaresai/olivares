// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

func parseOne(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "t.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}

func TestEmptyCombinedClosureDoesNotPanic(t *testing.T) {
	src := `package main
func combinedPurchaseView() interface{} {
	return func(p int) bool {}
}`
	f := parseOne(t, src)
	fd := findFunc(f, "combinedPurchaseView")
	if fd == nil {
		t.Fatal("missing func")
	}
	rep := &report{Checks: map[string]check{}}
	checkCombined(rep, fd)
	if rep.Checks["combined.present"].OK != true {
		t.Fatalf("empty closure is parseable and present: %+v", rep.Checks)
	}
	if rep.Checks["combined.fallback_is_fromClaims"].OK {
		t.Fatal("empty closure must fail the fallback predicate")
	}
	if !strings.Contains(rep.Checks["combined.fallback_is_fromClaims"].Detail, "empty") {
		t.Fatalf("want a review reason, got %q", rep.Checks["combined.fallback_is_fromClaims"].Detail)
	}
}

func TestBareDurableReturnDoesNotPanic(t *testing.T) {
	src := `package main
func durableLicensed() bool {
	return
}`
	f := parseOne(t, src)
	fd := findFunc(f, "durableLicensed")
	if fd == nil {
		t.Fatal("missing func")
	}
	rep := &report{Checks: map[string]check{}}
	checkDurable(rep, fd, nil)
	if rep.Checks["durable.present"].OK != true {
		t.Fatalf("bare return is parseable and present: %+v", rep.Checks)
	}
	if rep.Checks["durable.denies_are_false"].OK {
		t.Fatal("bare return must fail denies_are_false")
	}
	if !strings.Contains(rep.Checks["durable.denies_are_false"].Detail, "bare return") {
		t.Fatalf("want a review reason, got %q", rep.Checks["durable.denies_are_false"].Detail)
	}
}

func TestSerialiseRejectsUnsupportedKind(t *testing.T) {
	ch := make(chan int)
	var b strings.Builder
	err := serialise(&b, reflect.ValueOf(ch))
	if err == nil {
		t.Fatalf("chan must be unsupported, got %q", b.String())
	}
	if !strings.Contains(err.Error(), "unsupported kind") {
		t.Fatalf("got %v", err)
	}
}

func TestSerialiseMapsAreCanonical(t *testing.T) {
	m := map[string]int{"b": 2, "a": 1}
	var b strings.Builder
	if err := serialise(&b, reflect.ValueOf(m)); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, `{2|"a"=>int:1 "b"=>int:2}`) {
		t.Fatalf("unsorted or unframed map: %s", got)
	}
}

func TestImportBindingsChangeDigest(t *testing.T) {
	src := `package main
func F() int { return activation.PackIdentityScale }`
	f := parseOne(t, src)
	fd := findFunc(f, "F")
	sameAST := []importBind{{Name: "activation", Path: "github.com/olivaresai/olivares/enterprise/activation"}}
	other := []importBind{{Name: "activation", Path: "github.com/example/not-activation"}}
	a, err := digestOf(sameAST, fd)
	if err != nil {
		t.Fatal(err)
	}
	b, err := digestOf(other, fd)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("the same function AST with an altered package import must not share a digest")
	}
	again, err := digestOf(sameAST, fd)
	if err != nil {
		t.Fatal(err)
	}
	if again != a {
		t.Fatal("digest must be stable")
	}
}

func TestSetCodePacksIdsMustBeIdentityScale(t *testing.T) {
	src := `package main
var setCodePacks = map[string]activation.Pack{
	"biz":  activation.PackBusiness,
	"reg":  activation.PackRegulated,
	"airs": activation.PackAIRuntimeSecurity,
	"cp":   activation.PackCompliancePacks,
	"ids":  activation.PackBusiness,
	"ent":  activation.PackEnterprise,
}`
	f := parseOne(t, src)
	rep := &report{Checks: map[string]check{}, SetCodePacks: map[string]string{}}
	checkSetCodePacks(rep, f, nil)
	if !rep.Checks["setpacks.present"].OK {
		t.Fatalf("present: %+v", rep.Checks)
	}
	if rep.Checks["setpacks.ids_is_identity_scale"].OK {
		t.Fatal("ids mapped to PackBusiness must fail")
	}
	if rep.Checks["setpacks.mapping_is_reviewed"].OK {
		t.Fatal("drifted mapping must fail")
	}
}

// A synthetic observed-crl-keyring-v1 durable selection: the reviewed wrapper and helper
// shapes, with no comments and no product bytes.
const observedCRLDurableSrc = `package main

func durableLicensed(licenseFile, dataDir string, getenv func(string) string) bool {
	return durableLicensedAt(licenseFile, dataDir, getenv, time.Now)
}

func durableLicensedAt(licenseFile, dataDir string, getenv func(string) string, now func() time.Time) bool {
	src, err := resolveLicense(licenseFile, dataDir, getenv)
	if err != nil || strings.TrimSpace(src.Blob) == "" {
		return false
	}
	holder := newDataDirLicenseHolder(dataDir, src, now, slog.Default())
	c, ok := holder.claims()
	if !ok || c.Status(now()) == license.StatusExpired {
		return false
	}
	gate := addongate.New("durablebus", holder.claims).
		WithRevocation(addongate.CRLView(crlViewFromDataDir(dataDir))).
		WithClock(now)
	if gate.State() == addongate.StateUnentitled {
		return false
	}
	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)
}
`

// The original direct construction, as the live-facts fixture writes it.
const legacyDurableSrc = `package main

func durableLicensed(licenseFile, dataDir string, getenv func(string) string) bool {
	src, err := resolveLicense(licenseFile, dataDir, getenv)
	if err != nil || strings.TrimSpace(src.Blob) == "" {
		return false
	}
	pub := license.DefaultPublicKey()
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	holder := newLicenseHolder(pub, src, nil, slog.Default())
	c, ok := holder.claims()
	if !ok || c.Status(time.Now()) == license.StatusExpired {
		return false
	}
	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)
}
`

func durableReport(t *testing.T, src string) *report {
	t.Helper()
	f := parseOne(t, src)
	rep := &report{Checks: map[string]check{}}
	checkDurable(rep, findFunc(f, "durableLicensed"), findFunc(f, "durableLicensedAt"))
	return rep
}

func requireAllDurable(t *testing.T, rep *report) {
	t.Helper()
	for _, name := range durableChecks {
		if c, ok := rep.Checks[name]; !ok || !c.OK {
			t.Errorf("%s: %+v", name, rep.Checks[name])
		}
	}
	if len(rep.Checks) != len(durableChecks) {
		t.Errorf("the durable program must set exactly its nine predicates, set %d", len(rep.Checks))
	}
}

func TestObservedCRLConstructionHoldsEveryDurablePredicate(t *testing.T) {
	rep := durableReport(t, observedCRLDurableSrc)
	if rep.Construction != constructionCurrent {
		t.Fatalf("construction %q", rep.Construction)
	}
	requireAllDurable(t, rep)
	if got := rep.Checks["durable.pack"].Detail; got != "PackIdentityScale" {
		t.Fatalf("durable.pack detail %q", got)
	}
}

func TestLegacyConstructionKeepsTheOriginalProgram(t *testing.T) {
	rep := durableReport(t, legacyDurableSrc)
	if rep.Construction != constructionLegacy {
		t.Fatalf("construction %q", rep.Construction)
	}
	requireAllDurable(t, rep)
}

// Every mutation must fail the named predicate; the purchase pack is data the evaluator
// compares, so it is asserted through durable.pack's detail instead.
func TestObservedCRLMutantsFailTheirPredicate(t *testing.T) {
	const wrapperReturn = "\treturn durableLicensedAt(licenseFile, dataDir, getenv, time.Now)\n"
	const stateGuard = "\tif gate.State() == addongate.StateUnentitled {\n\t\treturn false\n\t}\n"
	const finalReturn = "\treturn combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)\n"
	cases := []struct{ name, old, new, check string }{
		{"wrapper forwards a nil clock", "getenv, time.Now)", "getenv, nil)", "durable.denies_are_false"},
		{"wrapper forwards another clock", "getenv, time.Now)", "getenv, durableClock)", "durable.statement_sequence_is_reviewed"},
		{"wrapper permutes an argument", "durableLicensedAt(licenseFile, dataDir,", "durableLicensedAt(dataDir, licenseFile,", "durable.denies_are_false"},
		{"wrapper invokes another function", "return durableLicensedAt(", "return durableLicensedAtUngated(", "durable.denies_are_false"},
		{"wrapper returns true", wrapperReturn, "\treturn true\n", "durable.denies_are_false"},
		{"wrapper gains a statement", wrapperReturn, "\t_ = getenv\n" + wrapperReturn, "durable.statement_sequence_is_reviewed"},
		{"wrapper signature changes", "func durableLicensed(licenseFile, dataDir string,", "func durableLicensed(licenseFile string, dataDir string,", "durable.statement_sequence_is_reviewed"},
		{"resolve guard removed", "\tif err != nil || strings.TrimSpace(src.Blob) == \"\" {\n\t\treturn false\n\t}\n", "", "durable.resolve_and_blob_denies"},
		{"resolve refusal made true", "strings.TrimSpace(src.Blob) == \"\" {\n\t\treturn false", "strings.TrimSpace(src.Blob) == \"\" {\n\t\treturn true", "durable.denies_are_false"},
		{"claims read before the holder", "\tholder := newDataDirLicenseHolder(dataDir, src, now, slog.Default())\n\tc, ok := holder.claims()\n", "\tc, ok := holder.claims()\n\tholder := newDataDirLicenseHolder(dataDir, src, now, slog.Default())\n", "durable.claims_are_read"},
		{"verification holder bypassed", "newDataDirLicenseHolder(dataDir, src, now, slog.Default())", "newLicenseHolder(license.DefaultPublicKey(), src, now, slog.Default())", "durable.invalid_key_denies"},
		{"holder uses the production clock", "newDataDirLicenseHolder(dataDir, src, now, slog.Default())", "newDataDirLicenseHolder(dataDir, src, time.Now, slog.Default())", "durable.invalid_key_denies"},
		{"term refusal removed", "\tif !ok || c.Status(now()) == license.StatusExpired {\n\t\treturn false\n\t}\n", "", "durable.term_guard_denies"},
		{"term compares another status", "license.StatusExpired", "license.StatusGrace", "durable.term_guard_denies"},
		{"term reads another clock", "c.Status(now())", "c.Status(time.Now())", "durable.term_guard_denies"},
		{"revocation removed", "\n\t\tWithRevocation(addongate.CRLView(crlViewFromDataDir(dataDir))).", "", "durable.statement_sequence_is_reviewed"},
		{"revocation replaced", "WithRevocation(addongate.CRLView(crlViewFromDataDir(dataDir)))", "WithRevocation(nil)", "durable.statement_sequence_is_reviewed"},
		{"revocation reads another directory", "crlViewFromDataDir(dataDir)", "crlViewFromDataDir(licenseFile)", "durable.statement_sequence_is_reviewed"},
		{"clock dropped", ".\n\t\tWithClock(now)", "", "durable.statement_sequence_is_reviewed"},
		{"clock changed", "WithClock(now)", "WithClock(time.Now)", "durable.statement_sequence_is_reviewed"},
		{"another state compared", "addongate.StateUnentitled", "addongate.StateGrace", "durable.statement_sequence_is_reviewed"},
		{"state comparison inverted", "gate.State() == addongate", "gate.State() != addongate", "durable.statement_sequence_is_reviewed"},
		{"pack inserted", "WithClock(now)", "WithClock(now).WithPack(activation.PackIdentityScale)", "durable.statement_sequence_is_reviewed"},
		{"entitlement inserted", "WithClock(now)", "WithClock(now).WithEntitlement(holder.grants)", "durable.statement_sequence_is_reviewed"},
		{"global gate installed", stateGuard, "\taddongate.Install(gate)\n" + stateGuard, "durable.statement_sequence_is_reviewed"},
		{"return before the state guard", stateGuard + finalReturn, finalReturn + stateGuard, "durable.denies_are_false"},
		{"state refusal made true", "addongate.StateUnentitled {\n\t\treturn false", "addongate.StateUnentitled {\n\t\treturn true", "durable.denies_are_false"},
		{"claims and grants reversed", "combinedPurchaseView(holder.claims, holder.grants)", "combinedPurchaseView(holder.grants, holder.claims)", "durable.selection_is_composition"},
		{"composition bypassed", "combinedPurchaseView(holder.claims, holder.grants)", "purchaseViewFromClaims(holder.claims)", "durable.selection_is_composition"},
		{"final selection returns true", finalReturn, "\treturn true\n", "durable.selection_is_composition"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(observedCRLDurableSrc, tc.old) != 1 {
				t.Fatalf("mutation anchor occurs %d times: %q", strings.Count(observedCRLDurableSrc, tc.old), tc.old)
			}
			rep := durableReport(t, strings.Replace(observedCRLDurableSrc, tc.old, tc.new, 1))
			if rep.Construction != constructionCurrent {
				t.Fatalf("construction %q", rep.Construction)
			}
			if c := rep.Checks[tc.check]; c.OK {
				t.Fatalf("%s still holds: %+v", tc.check, rep.Checks)
			}
		})
	}
}

func TestPurchasePackIsReportedForTheEvaluator(t *testing.T) {
	rep := durableReport(t, strings.Replace(observedCRLDurableSrc,
		"activation.PackIdentityScale)", "activation.PackBusiness)", 1))
	if got := rep.Checks["durable.pack"].Detail; got != "PackBusiness" {
		t.Fatalf("durable.pack detail %q", got)
	}
}

// A comment is not structure: decoys naming every reviewed call cannot make a helper that
// lost its revocation gate pass, and cannot move a helper that kept it.
func TestCommentDecoysCannotSatisfyStructure(t *testing.T) {
	decoy := "\t// return true; WithRevocation(addongate.CRLView(crlViewFromDataDir(dataDir))); WithClock(now)\n"
	withDecoy := strings.Replace(observedCRLDurableSrc, "\tif gate.State()", decoy+"\tif gate.State()", 1)
	requireAllDurable(t, durableReport(t, withDecoy))
	lost := strings.Replace(withDecoy, "\n\t\tWithRevocation(addongate.CRLView(crlViewFromDataDir(dataDir))).", "", 1)
	if durableReport(t, lost).Checks["durable.statement_sequence_is_reviewed"].OK {
		t.Fatal("a comment naming WithRevocation satisfied the removed revocation gate")
	}
}

// The helper's presence is the only discriminant, and neither half borrows the other's pass.
func TestConstructionsCannotBeMixed(t *testing.T) {
	wrapperOnly := observedCRLDurableSrc[:strings.Index(observedCRLDurableSrc, "func durableLicensedAt")]
	rep := durableReport(t, wrapperOnly)
	if rep.Construction != constructionLegacy {
		t.Fatalf("a wrapper without its helper must be judged legacy, got %q", rep.Construction)
	}
	if rep.Checks["durable.selection_is_composition"].OK || rep.Checks["durable.statement_sequence_is_reviewed"].OK {
		t.Fatalf("a forwarding wrapper passed the legacy program: %+v", rep.Checks)
	}
	helper := observedCRLDurableSrc[strings.Index(observedCRLDurableSrc, "func durableLicensedAt"):]
	rep = durableReport(t, legacyDurableSrc+"\n"+helper)
	if rep.Construction != constructionCurrent {
		t.Fatalf("a present helper must select the current construction, got %q", rep.Construction)
	}
	if rep.Checks["durable.denies_are_false"].OK || rep.Checks["durable.statement_sequence_is_reviewed"].OK {
		t.Fatalf("a legacy wrapper passed beside the current helper: %+v", rep.Checks)
	}
	rep = durableReport(t, strings.Replace(observedCRLDurableSrc, "func durableLicensedAt(", "func durableLicensedAtV2(", 1))
	if rep.Construction != constructionLegacy || rep.Checks["durable.statement_sequence_is_reviewed"].OK {
		t.Fatalf("a renamed helper leaves the wrapper without its callee and must fail: %+v", rep.Checks)
	}
}

// The report schema moved; the digest preimage did not. Unchanged declarations keep their
// reviewed hashes only because the preimage still begins with overlay-ast/v1.
func TestDigestPreimageStaysV1(t *testing.T) {
	if readerSchema != "overlay-ast/v2" || digestSchema != "overlay-ast/v1" {
		t.Fatalf("reader %q digest %q", readerSchema, digestSchema)
	}
	f := parseOne(t, "package main\nfunc F() int { return 1 }")
	fd := findFunc(f, "F")
	imps := []importBind{{Name: "time", Path: "time"}}
	got, err := digestOf(imps, fd)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("overlay-ast/v1\n")
	writeImports(&b, imps)
	b.WriteByte('\n')
	if err := serialise(&b, reflect.ValueOf(ast.Node(fd))); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(b.String()))
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("digest preimage is no longer framed by overlay-ast/v1: %s != %s", got, want)
	}
}

func TestSurfaceIsTheElevenKeyUnion(t *testing.T) {
	keys := surfaceKeys()
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("duplicate surface key %s", k)
		}
		seen[k] = true
	}
	if len(keys) != 11 || !seen[pDurable+"#durableLicensedAt"] || !seen[pDurable+"#durableLicensed"] {
		t.Fatalf("surface %v", keys)
	}
}
