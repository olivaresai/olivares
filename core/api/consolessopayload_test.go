// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// THE FIELD THE CONSOLE NEVER SENT (C2).
//
// SAMLSPSignCertPEM had FIFTEEN production readers and ZERO tests. The console's SAML
// form collected the SP ENCRYPTION keypair and silently omitted the SIGNING one, so the
// stored config kept its sealed signing KEY (PutConfigIdP preserves a blank secret) while
// the public signing CERT was replaced verbatim with "". That half-pair is not a degraded
// config, it is a DEAD one: samlFromParts enters the signing branch on `cert != "" || key
// != ""` and loadSigningKeypair then rejects the missing half with ErrNotConfigured, so
// the provider never builds and nobody can log in. An operator configuring SSO from the
// console broke their own login, and every test stayed green.
//
// WHY THE EXISTING GUARDS COULD NOT SEE IT. ssoschema_parity_test.go derives the published
// OpenAPI schema from these Go structs, so the field was declared, documented and
// published — correctly. consoleroutes_test.go proves the console calls routes the engine
// registers — it did. Neither looks at the PAYLOAD, and web tests run against mocks that
// accept whatever the console sends. The gap was never a missing check on either side; it
// was that nothing compared the two.
//
// WHY IT PARSES THE CONSOLE SOURCE INSTEAD OF RESTATING THE PAYLOAD. A contract test whose
// bytes are copied by hand only ever watches ONE side move: it pins the engine while the
// console drifts away underneath it. The payload here is read out of the real
// buildInput(), so deleting the field from the console turns this red rather than leaving
// it green against a copy of the old truth.
//
// It then decodes that payload with the engine's decoder SETTINGS (DisallowUnknownFields,
// mirroring render.go), so a key the console invents fails here rather than 400-ing in
// front of an operator. Note it REPRODUCES those settings rather than calling decodeJSON,
// so a change to decodeJSON itself would not be reflected.
//
// ⚠ SCOPE, measured — a static parser is not the console running. It reads the SAML return
// literal; it does not evaluate JavaScript, so it cannot see a value that is computed,
// conditional, or spread from a helper it does not know. The behavioral guard for that is
// the SAML save test in web/src/features/console/console.test.tsx, which drives the real
// form and asserts what putSSO actually receives. THE TWO ARE NOT REDUNDANT: this one
// derives the pairs from the engine (so a new keypair is demanded on the day it lands),
// the other proves the payload really leaves the component. Keep both.
const ssoTabRelPath = "../../web/src/features/console/sso-tab.tsx"

// objectKeyRe matches an object-literal key on its own line: `saml_sp_cert_pem: spCert...`.
// Keys are snake_case wire names; the console's local variables are camelCase, so this
// cannot accidentally collect an identifier from the right-hand side.
var objectKeyRe = regexp.MustCompile(`(?m)^\s*([a-z][a-z0-9]*(?:_[a-z0-9]+)+)\s*:`)

// TestConsolePayloadCarriesBothHalvesOfEverySAMLKeypair is the guard for C2.
//
// The pairs are DERIVED from ssoConfigInput by reflection, not listed here: every
// `*_cert_pem` the engine accepts must have its `*_key_pem` sibling, and the console must
// send BOTH. A third keypair added to the engine tomorrow is covered on the day it lands,
// without anyone remembering to extend a list — the failure mode that let this one through.
func TestConsolePayloadCarriesBothHalvesOfEverySAMLKeypair(t *testing.T) {
	sent := consoleSAMLPayloadKeys(t)

	// Positive control: a parser that silently matches nothing would satisfy every
	// assertion below by vacuity. Anchor it on a field that predates this test, so the
	// control cannot be satisfied by the very field the test was written to require.
	if !sent["saml_sp_cert_pem"] {
		t.Fatalf("parser found no saml_sp_cert_pem in %s — the extraction is broken, so every check below would pass vacuously (found %d keys: %v)",
			ssoTabRelPath, len(sent), sortedKeys(sent))
	}

	accepted := jsonTagsOf(ssoConfigInput{})
	var pairs int
	for certField := range accepted {
		if !strings.HasSuffix(certField, "_cert_pem") {
			continue
		}
		keyField := strings.TrimSuffix(certField, "_cert_pem") + "_key_pem"
		if !accepted[keyField] {
			continue // not a keypair: a cert the engine takes without a private half
		}
		pairs++
		for _, half := range [...]string{certField, keyField} {
			if !sent[half] {
				t.Errorf("the console never sends %q.\n"+
					"It is one half of the (%s, %s) SP keypair the engine accepts, and a half-pair does NOT\n"+
					"degrade to 'unsigned' — core/auth/federation/saml.go enters the branch on `cert != \"\" || key != \"\"`\n"+
					"and then rejects the missing half with ErrNotConfigured, so the provider fails to build and\n"+
					"LOGIN STOPS. Send it from buildInput() in %s.",
					half, certField, keyField, ssoTabRelPath)
			}
		}
	}
	if pairs < 2 {
		t.Fatalf("expected the engine to accept at least the encryption and signing SP keypairs, derived %d — "+
			"if a keypair was removed from ssoConfigInput this test just stopped guarding it", pairs)
	}

	// Every key the console sends must survive the engine's REAL decoder settings. A
	// payload with an unknown field is a 400 for the operator, not a type error for us.
	if err := decodeLikeTheEngine(t, sent); err != nil {
		t.Fatalf("the console's payload does not decode into ssoConfigInput: %v\n"+
			"decodeJSON (core/api/render.go) sets DisallowUnknownFields, so this is a 400 \"invalid JSON body\" in production.", err)
	}
}

// consoleSAMLPayloadKeys returns the wire field names the console's SAML save actually
// sends: the keys of buildInput()'s SAML return object plus the `posture` object it
// spreads into it (`...posture`), which is where the protocol-independent fields live.
func consoleSAMLPayloadKeys(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(filepath.FromSlash(ssoTabRelPath))
	if err != nil {
		t.Fatalf("read console SSO tab: %v", err)
	}
	text := string(src)

	// BRANCH-AWARE, and it has to be. The first version of this scanned the WHOLE
	// buildInput() body and unioned every key it found, which a contrast refuted by moving
	// the property to the OIDC return: the SAML branch stopped sending it and this stayed
	// green (external contrast, Q1). buildInput() has two
	// returns; only the SECOND is the SAML payload. Scan that one alone.
	body, ok := blockAfter(text, "function buildInput(): SSOConfigInput {")
	if !ok {
		t.Fatalf("could not find buildInput() in %s. The console was restructured; fix this parser "+
			"rather than deleting the guard — an unparsed payload is not a verified one.", ssoTabRelPath)
	}
	oidc, ok := blockAfter(body, "if (protocol === 'oidc') {")
	if !ok {
		t.Fatalf("could not find the OIDC branch inside buildInput() in %s; without it the SAML "+
			"payload cannot be isolated and this guard would union both branches again.", ssoTabRelPath)
	}
	samlPart := body[strings.Index(body, oidc)+len(oidc):]

	keys := map[string]bool{}
	for _, m := range objectKeyRe.FindAllStringSubmatch(samlPart, -1) {
		keys[m[1]] = true
	}
	// The SAML return spreads `posture`, whose keys live in an object declared above.
	posture, ok := blockAfter(text, "const posture = {")
	if !ok {
		t.Fatalf("could not find the spread posture object in %s", ssoTabRelPath)
	}
	if strings.Contains(samlPart, "...posture") {
		for _, m := range objectKeyRe.FindAllStringSubmatch(posture, -1) {
			keys[m[1]] = true
		}
	}
	return keys
}

// blockAfter returns the source between the brace opened at the end of `opener` and its
// matching close, so nested objects inside the block are included whole.
func blockAfter(text, opener string) (string, bool) {
	i := strings.Index(text, opener)
	if i < 0 {
		return "", false
	}
	start := i + len(opener) - 1 // the '{' that opener ends with
	depth := 0
	for j := start; j < len(text); j++ {
		switch text[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start+1 : j], true
			}
		}
	}
	return "", false
}

// jsonTagsOf returns the wire names a request struct accepts.
func jsonTagsOf(v any) map[string]bool {
	out := map[string]bool{}
	rt := reflect.TypeOf(v)
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// decodeLikeTheEngine builds a payload carrying every key the console sends, typed from
// ssoConfigInput's own fields, and decodes it exactly as the handler does.
func decodeLikeTheEngine(t *testing.T, sent map[string]bool) error {
	t.Helper()
	rt := reflect.TypeOf(ssoConfigInput{})
	byTag := map[string]reflect.Kind{}
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		byTag[name] = rt.Field(i).Type.Kind()
	}
	payload := map[string]any{}
	for k := range sent {
		switch byTag[k] {
		case reflect.Bool:
			payload[k] = true
		case reflect.Slice:
			payload[k] = []string{}
		case reflect.String:
			payload[k] = "x"
		default:
			// Unknown to the engine: send a string so DisallowUnknownFields is what
			// rejects it, naming the field, rather than a type mismatch.
			payload[k] = "x"
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in ssoConfigInput
	return dec.Decode(&in)
}

func sortedKeys(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return fmt.Sprint(out)
}
