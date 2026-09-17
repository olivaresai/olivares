// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// CP1 (docs/design/configured-pdp-startup.md): invalid EXPLICIT external-PDP
// configuration is a construction error, not a silent downgrade to a native-only
// module set. These tests drive the actual private loaders and the actual
// buildModules composition root. None of them performs an HTTP request: OPA
// construction builds a client and validates its two settings, nothing else.

// Synthetic markers. They are unmistakable in a diagnostic and appear in no other
// file, so an assertion that an error or a log omits them cannot pass by accident.
// This is a diagnostic-exposure check, not a secret-scanner benchmark.
const (
	pdpMarkerEngine = "cp1-marker-engine"
	pdpMarkerPolicy = "cp1-marker-policy-source"
	pdpMarkerPath   = "cp1-marker-policy-file"
	pdpMarkerURL    = "http://cp1-marker-host.invalid:65535"
	pdpMarkerToken  = "cp1-marker-bearer-token"
)

// pdpForbidSecret restricts exactly one request shape, so a test can tell the
// configured overlay apart from the native engine by the DECISION, not by counting
// options.
const pdpForbidSecret = `forbid(principal, action, resource) when { resource.sensitivity == "secret" };`

// pdpGetenv injects a fixed environment into the loaders under test so a case never
// depends on the ambient process environment. An unset key reads as "".
func pdpGetenv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// pdpCaptureLog returns a logger whose every record is retained for inspection.
func pdpCaptureLog() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// pdpPolicyFile writes an owned policy file under the test's own temporary directory
// and returns its path. It makes no assumption about the running user's privileges:
// unreadability is expressed as a path that does not exist, never as a chmod.
func pdpPolicyFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// pdpTestSigner is the ephemeral audit signer buildModules requires. No assertion
// here depends on the key material.
func pdpTestSigner(t *testing.T) *audit.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// pdpSecretRequest is the request the forbid rule above matches when sensitivity is
// "secret" and leaves untouched otherwise.
func pdpSecretRequest(sensitivity string) auth.Request {
	return auth.Request{
		Principal:  auth.Principal{Kind: auth.KindUser, CredID: "cred-cp1"},
		Permission: "agent:write",
		Tenant:     model.TenantID("t-cp1"),
		Resource:   auth.ResourceAttrs{Kind: "agent", ID: "a-cp1", Sensitivity: sensitivity},
	}
}

// TestGovernancePDPValidConfigurationsAreRetained pins the configurations that must
// keep building exactly as they did before CP1. "Options" is the observable: 0 means
// the native ABAC engine alone, 1 means the configured deny-overlay survived the
// loader. Nothing here may become an error as a side effect of rejecting invalid
// input — including the malformed-but-nonempty OPA URL, which construction does not
// parse today and which CP1 does not start parsing.
func TestGovernancePDPValidConfigurationsAreRetained(t *testing.T) {
	valid := pdpPolicyFile(t, "valid.cedar", pdpForbidSecret)
	empty := pdpPolicyFile(t, "empty.cedar", "")

	for _, tc := range []struct {
		name string
		env  map[string]string
		want int
	}{
		{"engine unset is native-only", map[string]string{}, 0},
		{"blank engine is native-only", map[string]string{"OLIVARES_PDP_ENGINE": "   "}, 0},
		{"none is native-only", map[string]string{"OLIVARES_PDP_ENGINE": "none"}, 0},
		{"mixed-case padded none is native-only", map[string]string{"OLIVARES_PDP_ENGINE": "  NoNe  "}, 0},
		{"cedar without a file is the empty overlay", map[string]string{
			"OLIVARES_PDP_ENGINE": "cedar"}, 1},
		{"cedar with a blank file setting is the empty overlay", map[string]string{
			"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": "   "}, 1},
		{"cedar with a readable empty file", map[string]string{
			"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": empty}, 1},
		{"cedar with a valid nonempty policy", map[string]string{
			"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": valid}, 1},
		{"cedar selector keeps its trim and case folding", map[string]string{
			"OLIVARES_PDP_ENGINE": "  CeDaR  ", "OLIVARES_PDP_CEDAR_FILE": valid}, 1},
		{"opa with a dotted decision path", map[string]string{
			"OLIVARES_PDP_ENGINE":   "opa",
			"OLIVARES_PDP_OPA_URL":  "http://127.0.0.1:8181",
			"OLIVARES_PDP_OPA_PATH": "authz.allow"}, 1},
		{"opa with a slashed decision path and trailing slashes", map[string]string{
			"OLIVARES_PDP_ENGINE":   "opa",
			"OLIVARES_PDP_OPA_URL":  "http://127.0.0.1:8181/",
			"OLIVARES_PDP_OPA_PATH": "/authz/allow/"}, 1},
		{"opa selector keeps its trim and case folding, token optional", map[string]string{
			"OLIVARES_PDP_ENGINE":    " OPA ",
			"OLIVARES_PDP_OPA_URL":   " http://127.0.0.1:8181 ",
			"OLIVARES_PDP_OPA_PATH":  " authz.allow ",
			"OLIVARES_PDP_OPA_TOKEN": pdpMarkerToken}, 1},
		{"malformed but nonempty opa url is NOT a construction error", map[string]string{
			"OLIVARES_PDP_ENGINE":   "opa",
			"OLIVARES_PDP_OPA_URL":  "::not a url::",
			"OLIVARES_PDP_OPA_PATH": "authz.allow"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := loadGovernancePDP(pdpGetenv(tc.env), discardLog())
			if err != nil {
				t.Fatalf("valid configuration was rejected: %v", err)
			}
			if len(opts) != tc.want {
				t.Fatalf("option count = %d, want %d", len(opts), tc.want)
			}
		})
	}
}

// TestGovernancePDPInvalidConfigurationsAreClassified asserts both halves of the CP1
// decision: the loader returns an error, and it returns the RIGHT error class, so an
// operator can tell an unreadable file from a bad policy, bad OPA settings or an
// unknown selector without the message quoting what they typed.
//
// The OPA rows are exactly the constructor's normalization: the base URL is trimmed
// of whitespace and trailing slashes, and the decision path is trimmed, has its dots
// replaced by slashes and its boundary slashes trimmed. Both must stay nonempty, and
// the decision path has no default.
func TestGovernancePDPInvalidConfigurationsAreClassified(t *testing.T) {
	malformed := pdpPolicyFile(t, "malformed.cedar", "forbid(principal")
	absent := filepath.Join(t.TempDir(), "absent.cedar")

	for _, tc := range []struct {
		name string
		env  map[string]string
		want error
	}{
		{"cedar file cannot be read", map[string]string{
			"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": absent},
			errPDPCedarFileUnreadable},
		{"cedar policy does not parse", map[string]string{
			"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": malformed},
			errPDPCedarPolicyInvalid},
		{"opa base url unset", map[string]string{
			"OLIVARES_PDP_ENGINE": "opa", "OLIVARES_PDP_OPA_PATH": "authz.allow"},
			errPDPOPASettingsInvalid},
		{"opa base url is whitespace", map[string]string{
			"OLIVARES_PDP_ENGINE":   "opa",
			"OLIVARES_PDP_OPA_URL":  "   ",
			"OLIVARES_PDP_OPA_PATH": "authz.allow"},
			errPDPOPASettingsInvalid},
		{"opa base url is slashes only", map[string]string{
			"OLIVARES_PDP_ENGINE":   "opa",
			"OLIVARES_PDP_OPA_URL":  " /// ",
			"OLIVARES_PDP_OPA_PATH": "authz.allow"},
			errPDPOPASettingsInvalid},
		{"opa decision path unset", map[string]string{
			"OLIVARES_PDP_ENGINE":  "opa",
			"OLIVARES_PDP_OPA_URL": "http://127.0.0.1:8181"},
			errPDPOPASettingsInvalid},
		{"opa decision path is whitespace", map[string]string{
			"OLIVARES_PDP_ENGINE":   "opa",
			"OLIVARES_PDP_OPA_URL":  "http://127.0.0.1:8181",
			"OLIVARES_PDP_OPA_PATH": "  "},
			errPDPOPASettingsInvalid},
		{"opa decision path is slashes only", map[string]string{
			"OLIVARES_PDP_ENGINE":   "opa",
			"OLIVARES_PDP_OPA_URL":  "http://127.0.0.1:8181",
			"OLIVARES_PDP_OPA_PATH": "/"},
			errPDPOPASettingsInvalid},
		{"opa decision path is a single dot", map[string]string{
			"OLIVARES_PDP_ENGINE":   "opa",
			"OLIVARES_PDP_OPA_URL":  "http://127.0.0.1:8181",
			"OLIVARES_PDP_OPA_PATH": "."},
			errPDPOPASettingsInvalid},
		{"opa decision path is dots and slashes only", map[string]string{
			"OLIVARES_PDP_ENGINE":   "opa",
			"OLIVARES_PDP_OPA_URL":  "http://127.0.0.1:8181",
			"OLIVARES_PDP_OPA_PATH": " ./.. "},
			errPDPOPASettingsInvalid},
		{"unsupported engine", map[string]string{
			"OLIVARES_PDP_ENGINE": pdpMarkerEngine},
			errPDPEngineUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := loadGovernancePDP(pdpGetenv(tc.env), discardLog())
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if len(opts) != 0 {
				t.Fatalf("a refused configuration still returned %d option(s)", len(opts))
			}
			// The same verdict must survive the outer loader, which is what the
			// composition root actually calls.
			opts, err = loadGovernanceOptions(pdpGetenv(tc.env), discardLog())
			if !errors.Is(err, tc.want) || len(opts) != 0 {
				t.Fatalf("loadGovernanceOptions = (%d option(s), %v), want (0, %v)", len(opts), err, tc.want)
			}
		})
	}
}

// TestGovernancePDPDiagnosticsOmitOperatorValues checks the privacy half of the
// contract. Every rejected case here carries a marker in the setting the operator
// supplied — the policy file path, the policy source itself, the OPA URL, the bearer
// token, the engine string — and neither the returned error nor anything the loader
// logged may contain it. The pairing matters for OPA: the marker URL and token are
// combined with an EMPTY decision path, because a malformed URL alone is not a
// construction error.
func TestGovernancePDPDiagnosticsOmitOperatorValues(t *testing.T) {
	markerPolicy := pdpPolicyFile(t, pdpMarkerPath+".cedar", pdpMarkerPolicy)

	for _, tc := range []struct {
		name    string
		env     map[string]string
		markers []string
	}{
		{"unreadable path is not echoed", map[string]string{
			"OLIVARES_PDP_ENGINE":     "cedar",
			"OLIVARES_PDP_CEDAR_FILE": filepath.Join(t.TempDir(), pdpMarkerPath+"-absent.cedar")},
			[]string{pdpMarkerPath}},
		{"policy source and path are not echoed", map[string]string{
			"OLIVARES_PDP_ENGINE":     "cedar",
			"OLIVARES_PDP_CEDAR_FILE": markerPolicy},
			[]string{pdpMarkerPath, pdpMarkerPolicy}},
		{"opa url and token are not echoed", map[string]string{
			"OLIVARES_PDP_ENGINE":    "opa",
			"OLIVARES_PDP_OPA_URL":   pdpMarkerURL,
			"OLIVARES_PDP_OPA_TOKEN": pdpMarkerToken,
			"OLIVARES_PDP_OPA_PATH":  ""},
			[]string{pdpMarkerURL, pdpMarkerToken, "cp1-marker-host"}},
		{"unsupported selector is not echoed", map[string]string{
			"OLIVARES_PDP_ENGINE": pdpMarkerEngine},
			[]string{pdpMarkerEngine}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log, captured := pdpCaptureLog()
			_, err := loadGovernanceOptions(pdpGetenv(tc.env), log)
			if err == nil {
				t.Fatal("invalid configuration returned no error")
			}
			for _, marker := range tc.markers {
				if strings.Contains(err.Error(), marker) {
					t.Errorf("returned error exposes %q: %v", marker, err)
				}
				if strings.Contains(captured.String(), marker) {
					t.Errorf("loader log exposes %q: %s", marker, captured.String())
				}
			}
			// Fixed and actionable: the message names the setting to correct.
			if !strings.Contains(err.Error(), "OLIVARES_PDP_") {
				t.Errorf("error does not name the configuration setting: %v", err)
			}
			// The caller reports the returned error, so the loader must not log a
			// duplicate copy of the same failure.
			if strings.Contains(captured.String(), "level=ERROR") {
				t.Errorf("loader logged a duplicate failure record: %s", captured.String())
			}
		})
	}
}

// TestConfiguredCedarForbidReachesGovernanceEvaluator proves the retained option is
// the real thing rather than a count: the module built from it DENIES the request the
// operator's forbid rule matches, and the same request is allowed when the operator
// selected none. Nothing here is mocked — it is governance.New and its request
// evaluator.
func TestConfiguredCedarForbidReachesGovernanceEvaluator(t *testing.T) {
	ctx := context.Background()
	policy := pdpPolicyFile(t, "forbid.cedar", pdpForbidSecret)

	configured, err := loadGovernanceOptions(pdpGetenv(map[string]string{
		"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": policy}), discardLog())
	if err != nil {
		t.Fatalf("valid cedar configuration was rejected: %v", err)
	}
	eval := governance.New(configured...).RequestEvaluator()

	dec, err := eval.Evaluate(ctx, pdpSecretRequest("secret"))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec.Allow {
		t.Fatal("the configured cedar forbid did not reach the governance evaluator")
	}
	if dec, err = eval.Evaluate(ctx, pdpSecretRequest("public")); err != nil || !dec.Allow {
		t.Fatalf("the configured overlay must restrict nothing else: %+v, %v", dec, err)
	}

	// Control: with none selected there is no overlay, so the same request stands.
	native, err := loadGovernanceOptions(pdpGetenv(map[string]string{
		"OLIVARES_PDP_ENGINE": "none"}), discardLog())
	if err != nil || len(native) != 0 {
		t.Fatalf("none must build the native-only option set: %d option(s), %v", len(native), err)
	}
	if dec, err = governance.New(native...).RequestEvaluator().Evaluate(ctx, pdpSecretRequest("secret")); err != nil || !dec.Allow {
		t.Fatalf("native-only construction must not carry the forbid: %+v, %v", dec, err)
	}

	// An empty overlay is a VALID configuration and restricts nothing either, so the
	// deny above is attributable to the policy and not to selecting cedar at all.
	empty, err := loadGovernanceOptions(pdpGetenv(map[string]string{
		"OLIVARES_PDP_ENGINE": "cedar"}), discardLog())
	if err != nil || len(empty) != 1 {
		t.Fatalf("empty cedar must build one option: %d option(s), %v", len(empty), err)
	}
	if dec, err = governance.New(empty...).RequestEvaluator().Evaluate(ctx, pdpSecretRequest("secret")); err != nil || !dec.Allow {
		t.Fatalf("the empty cedar overlay must impose no restriction: %+v, %v", dec, err)
	}
}

// TestGovernancePDPErrorPrecedesStalenessValidation fixes the precedence when a
// deployment gets BOTH settings wrong: the policy engine is reported first. The
// existing staleness contract is unchanged and still has its own regression in
// TestPolicyMaxStalenessConfigFailsClosed; this only pins the order, and that a valid
// pair still yields both options.
func TestGovernancePDPErrorPrecedesStalenessValidation(t *testing.T) {
	both := map[string]string{
		"OLIVARES_PDP_ENGINE":           pdpMarkerEngine,
		"OLIVARES_POLICY_MAX_STALENESS": "not-a-duration",
	}
	opts, err := loadGovernanceOptions(pdpGetenv(both), discardLog())
	if !errors.Is(err, errPDPEngineUnsupported) {
		t.Fatalf("error = %v, want the PDP class first", err)
	}
	if len(opts) != 0 {
		t.Fatalf("a refused configuration still returned %d option(s)", len(opts))
	}

	// A valid staleness bound alone still fails on the invalid engine.
	if _, err = loadGovernanceOptions(pdpGetenv(map[string]string{
		"OLIVARES_PDP_ENGINE":           pdpMarkerEngine,
		"OLIVARES_POLICY_MAX_STALENESS": "72h",
	}), discardLog()); !errors.Is(err, errPDPEngineUnsupported) {
		t.Fatalf("error = %v, want the PDP class", err)
	}

	// And a valid pair retains BOTH options: rejecting invalid input must not narrow
	// what a correct deployment gets.
	opts, err = loadGovernanceOptions(pdpGetenv(map[string]string{
		"OLIVARES_PDP_ENGINE":           "cedar",
		"OLIVARES_PDP_CEDAR_FILE":       pdpPolicyFile(t, "both.cedar", pdpForbidSecret),
		"OLIVARES_POLICY_MAX_STALENESS": "72h",
	}), discardLog())
	if err != nil || len(opts) != 2 {
		t.Fatalf("valid pair = (%d option(s), %v), want (2, nil)", len(opts), err)
	}
}

// TestInvalidExplicitPDPConfigIsNotSilentlyAccepted is the CP1 negative control, and
// it is RED against the R38 baseline: there loadGovernancePDP logged the failure and
// returned nil options, so an operator's invalid explicit policy engine reached
// governance.New as a native-only module set with no error anywhere in the chain.
// Each case asserts the composition root now refuses that configuration instead.
func TestInvalidExplicitPDPConfigIsNotSilentlyAccepted(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.cedar")
	malformed := filepath.Join(dir, "malformed.cedar")
	if err := os.WriteFile(malformed, []byte("forbid(principal"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"unreadable cedar file", map[string]string{
			"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": missing}},
		{"malformed cedar policy", map[string]string{
			"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": malformed}},
		{"opa without a decision path", map[string]string{
			"OLIVARES_PDP_ENGINE": "opa", "OLIVARES_PDP_OPA_URL": "http://127.0.0.1:8181"}},
		{"unsupported engine", map[string]string{
			"OLIVARES_PDP_ENGINE": "cp1-marker-engine"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := loadGovernanceOptions(pdpGetenv(tc.env), discardLog())
			if err == nil {
				t.Fatalf("invalid explicit PDP configuration was accepted: %d option(s), nil error", len(opts))
			}
			if len(opts) != 0 {
				t.Fatalf("a refused configuration still returned %d option(s)", len(opts))
			}
		})
	}
}

// TestBuildModulesRefusesInvalidExplicitPDPConfig drives the real composition root,
// not just the helper: buildModules reads the process environment through osGetenv,
// so this is the seam an operational command actually crosses. Against R38 it is RED
// (a full module set was returned with a nil error). The four production callers —
// boot, moduleOpenAPIDocument, collectSchemaManifest and bootSchemaRegistrar — each
// return this error wrapped with fixed context; that propagation is established by
// source inspection and documented, not by executing a server, a migration, a support
// bundle or a directory maintenance run here.
func TestBuildModulesRefusesInvalidExplicitPDPConfig(t *testing.T) {
	t.Setenv("OLIVARES_PDP_ENGINE", "cedar")
	t.Setenv("OLIVARES_PDP_CEDAR_FILE", filepath.Join(t.TempDir(), "absent.cedar"))

	set, err := buildModules(pdpTestSigner(t), nil, nil, nil, nil, sourcesConfig{}, EditionConfig{}, discardLog())
	if err == nil {
		t.Fatalf("buildModules accepted an unreadable configured Cedar policy: %d module(s)", len(set.all))
	}
	if !errors.Is(err, errPDPCedarFileUnreadable) {
		t.Fatalf("buildModules error = %v, want the unreadable-source class", err)
	}
	if len(set.all) != 0 || set.gov != nil {
		t.Fatalf("a refused construction returned a usable module set: %d module(s), governance=%t",
			len(set.all), set.gov != nil)
	}
}

// TestBuildModulesAcceptsValidConfiguredCedarPolicy is the other side of the
// compatibility statement: a deployment whose explicit configuration is VALID builds
// the same module set it built before CP1.
func TestBuildModulesAcceptsValidConfiguredCedarPolicy(t *testing.T) {
	t.Setenv("OLIVARES_PDP_ENGINE", "cedar")
	t.Setenv("OLIVARES_PDP_CEDAR_FILE", pdpPolicyFile(t, "valid.cedar", pdpForbidSecret))

	set, err := buildModules(pdpTestSigner(t), nil, nil, nil, nil, sourcesConfig{}, EditionConfig{}, discardLog())
	if err != nil {
		t.Fatalf("a valid configured Cedar policy was rejected: %v", err)
	}
	if len(set.all) == 0 || set.gov == nil {
		t.Fatalf("valid construction produced no usable module set: %d module(s), governance=%t",
			len(set.all), set.gov != nil)
	}
	dec, err := set.gov.RequestEvaluator().Evaluate(context.Background(), pdpSecretRequest("secret"))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec.Allow {
		t.Fatal("the configured forbid did not reach the constructed module set")
	}
}
