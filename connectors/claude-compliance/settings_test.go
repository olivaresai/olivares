// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudecompliance

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// oneOrg lists a single linked org so the per-org settings findings are unambiguous.
const oneOrgBody = `{"data":[{"uuid":"org_a","name":"Acme"}]}`

// settingsBody is a real effective_organization_settings response. It deliberately
// OMITS content_redaction_enabled (one of the five named controls) to exercise the
// "missing row => not introspectable, NEVER off" path, and includes an UNKNOWN type
// (future_widget) to exercise the honest forward-compatible fallback.
const settingsBody = `{
  "type":"effective_organization_settings",
  "organization_id":"org_a",
  "api_keys":[
    {"id":"ckey_active","name":"siem","is_active":true,"scopes":["read:compliance_activities","read:compliance_org_data"]},
    {"id":"ckey_old","name":"retired","is_active":false,"scopes":["read:compliance_org_data"]}
  ],
  "settings":[
    {"name":"code_execution_network_egress_enabled","type":"boolean","value":true},
    {"name":"ip_allowlist_enabled","type":"boolean","value":false},
    {"name":"ip_allowlist_ip_ranges","type":"string_list","value":["10.0.0.0/8","203.0.113.0/24"]},
    {"name":"sso_provisioning_mode","type":"provisioning_mode","value":"scim_advanced"},
    {"name":"account_session_duration_seconds","type":"integer","value":null},
    {"name":"data_retention_periods","type":"data_retention","value":{"chat":{"type":"fixed","timescale":"day","duration":90},"project":{"type":"indefinite"}}},
    {"name":"some_future_setting","type":"future_widget","value":{"x":1}}
  ]
}`

// settingsHandler answers the directory + settings endpoints. The directory side is
// minimal (one org, empty roles/users/groups) so the settings findings dominate.
func settingsHandler(t *testing.T, settingsStatus int, settingsBodyText string) func(*http.Request) (int, string) {
	t.Helper()
	return func(req *http.Request) (int, string) {
		if req.Method != http.MethodGet {
			t.Errorf("non-GET request %s — settings attestation must be read-only", req.Method)
		}
		if k := req.Header.Get("x-api-key"); k != "sk-ant-api01-cak" {
			t.Errorf("auth = %q, want the DISTINCT Compliance Access Key", k)
		}
		p := req.URL.Path
		switch {
		case strings.HasSuffix(p, "/settings"):
			return settingsStatus, settingsBodyText
		case strings.HasSuffix(p, "/roles"):
			return http.StatusOK, `{"data":[],"has_more":false}`
		case strings.HasSuffix(p, "/users"):
			return http.StatusOK, `{"data":[],"has_more":false}`
		case p == groupsPath:
			return http.StatusOK, `{"data":[],"has_more":false}`
		case p == orgsPath:
			return http.StatusOK, oneOrgBody
		case p == "/v1/compliance/apps/projects":
			return http.StatusOK, `{"data":[],"has_more":false}`
		default:
			t.Fatalf("unexpected path %q", p)
			return http.StatusInternalServerError, ""
		}
	}
}

// settingsFindings runs Gather with a CAK source and returns only the posture findings
// (Kind=posture) — the settings attestation, keyed by SubjectKind.
func settingsFindings(t *testing.T, doer *routeDoer) []model.FindingReport {
	t.Helper()
	s := newDirectorySource(t, doer)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var out []model.FindingReport
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			t.Fatalf("emitted %T, want FindingReport", o)
		}
		if f.Kind == findingKindSettings {
			out = append(out, f)
		}
	}
	return out
}

// titleFor returns the title of the first finding whose SubjectRef matches, or "".
func titleFor(fs []model.FindingReport, subjectRef string) string {
	for _, f := range fs {
		if f.SubjectRef == subjectRef {
			return f.Title
		}
	}
	return ""
}

// TestSettings_AttestsEnforcedControls proves the core CLA gap #5 behavior: each enforced
// setting row becomes one Info posture finding whose Title carries the legible enforced
// value, every value type is interpreted, an unknown type degrades honestly, and the
// compliance-key inventory is attested once. Read-only, minimal-data, sealed by hash.
func TestSettings_AttestsEnforcedControls(t *testing.T) {
	doer := &routeDoer{handler: settingsHandler(t, http.StatusOK, settingsBody)}
	fs := settingsFindings(t, doer)

	// Every settings finding carries a sealed DetailHash and a conservative severity
	// (Info evidence, or Low for a recognized security-weak enforced state — never higher).
	for _, f := range fs {
		if f.Severity != model.SeverityInfo && f.Severity != model.SeverityLow {
			t.Errorf("settings finding %q severity = %q, want info or low (evidence, never a spurious alert)", f.SubjectRef, f.Severity)
		}
		if f.DetailHash == "" {
			t.Errorf("settings finding %q missing DetailHash (tamper-evidence)", f.SubjectRef)
		}
	}

	// Conservative calibration: a security-WEAK enforced state is Low; a healthy/benign
	// one stays Info. (sin alarmismo — only unambiguous weakenings are raised.)
	sev := func(ref string) model.Severity {
		for _, f := range fs {
			if f.SubjectRef == ref {
				return f.Severity
			}
		}
		return ""
	}
	if got := sev("org_a#code_execution_network_egress_enabled"); got != model.SeverityLow {
		t.Errorf("code-exec egress ENABLED is a posture signal: severity = %q, want low", got)
	}
	if got := sev("org_a#ip_allowlist_enabled"); got != model.SeverityLow {
		t.Errorf("ip_allowlist DISABLED is a posture signal: severity = %q, want low", got)
	}
	if got := sev("org_a#sso_provisioning_mode"); got != model.SeverityInfo {
		t.Errorf("sso_provisioning_mode=scim_advanced is healthy: severity = %q, want info", got)
	}

	// An SSO control declares it resolves the claude-console unknown-by-api blind spot.
	if got := titleFor(fs, "org_a#sso_provisioning_mode"); !strings.Contains(got, "unknown-by-api") || !strings.Contains(got, "blind spot") {
		t.Errorf("sso control must declare it resolves the claude-console blind spot: %q", got)
	}

	// Each type is interpreted into a legible enforced value in the Title.
	cases := map[string]string{
		"org_a#code_execution_network_egress_enabled": "enforced: true",
		"org_a#ip_allowlist_enabled":                  "enforced: false",
		"org_a#ip_allowlist_ip_ranges":                "enforced: 2 entries",
		"org_a#sso_provisioning_mode":                 "enforced: scim_advanced",
		"org_a#account_session_duration_seconds":      "enforced: no-limit",
		"org_a#data_retention_periods":                "enforced: chat=90day(fixed);project=indefinite",
	}
	for ref, want := range cases {
		got := titleFor(fs, ref)
		if !strings.Contains(got, want) {
			t.Errorf("setting %s title = %q, want it to contain %q", ref, got, want)
		}
	}

	// An UNKNOWN value type is reported honestly, never guessed.
	if got := titleFor(fs, "org_a#some_future_setting"); !strings.Contains(got, "not interpreted") {
		t.Errorf("unknown setting type must degrade honestly, got %q", got)
	}

	// The compliance-key inventory is attested once, with active/deactivated counts.
	var keyFinding *model.FindingReport
	for i := range fs {
		if fs[i].SubjectKind == subjectKindComplianceKey {
			if keyFinding != nil {
				t.Errorf("compliance-key inventory must be emitted ONCE, found a duplicate")
			}
			keyFinding = &fs[i]
		}
	}
	if keyFinding == nil {
		t.Fatalf("want a compliance-key inventory finding")
	}
	if !strings.Contains(keyFinding.Title, "2 total") || !strings.Contains(keyFinding.Title, "1 active") || !strings.Contains(keyFinding.Title, "1 deactivated") {
		t.Errorf("compliance-key inventory title = %q, want 2 total/1 active/1 deactivated", keyFinding.Title)
	}

	// Minimal-data: the raw Compliance Access Key secret must never appear in a finding.
	for _, f := range fs {
		blob := f.Title + "|" + f.SubjectRef + "|" + f.DetailHash
		if strings.Contains(blob, "sk-ant-api01-cak") {
			t.Fatalf("setting finding leaked the credential: %q", f.Title)
		}
	}
}

// TestSettings_MissingRowIsNotOff is the keystone honesty test: a named control the
// response OMITS (content_redaction_enabled here) is reported as not-controllable /
// not-introspectable, and NEVER as "off"/"false"/"disabled".
func TestSettings_MissingRowIsNotOff(t *testing.T) {
	doer := &routeDoer{handler: settingsHandler(t, http.StatusOK, settingsBody)}
	fs := settingsFindings(t, doer)

	got := titleFor(fs, "org_a#content_redaction_enabled")
	if got == "" {
		t.Fatal("an omitted named control must still produce a not-introspectable finding")
	}
	low := strings.ToLower(got)
	if !strings.Contains(low, "introspectable") {
		t.Errorf("missing control must read not-controllable/introspectable, got %q", got)
	}
	// Keystone: an absent row must NEVER be attested to a value — it must not carry the
	// present-attestation marker "enforced:" (which is how a real false/off value reads),
	// because Anthropic's contract says a missing row is "not controllable", not "off".
	if strings.Contains(low, "enforced:") {
		t.Errorf("missing control MUST NOT be attested to a value (a missing row is not 'off'): %q", got)
	}

	// The four named controls that ARE present must NOT get a duplicate absence finding.
	for _, ref := range []string{
		"org_a#code_execution_network_egress_enabled",
		"org_a#ip_allowlist_enabled",
		"org_a#sso_provisioning_mode",
		"org_a#data_retention_periods",
	} {
		if title := titleFor(fs, ref); strings.Contains(strings.ToLower(title), "not controllable") {
			t.Errorf("present control %s must be attested, not marked not-controllable: %q", ref, title)
		}
	}
}

// TestSettings_ScopeMissing403 proves a 403 (the CAK lacks read:compliance_org_data)
// degrades to exactly one honest note naming the scope, the DIRECTORY evidence still
// emits alongside it (best-effort), and Gather does NOT fail (deny-closed, no fabrication).
func TestSettings_ScopeMissing403(t *testing.T) {
	doer := &routeDoer{handler: settingsHandler(t, http.StatusForbidden, `{"error":"forbidden"}`)}
	s := newDirectorySource(t, doer)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("a 403 on settings must not fail Gather (best-effort): %v", err)
	}

	var notes, dirFindings int
	for _, o := range sink.obs {
		f := o.(model.FindingReport)
		switch {
		case f.Kind == findingKindDirectory:
			dirFindings++ // the directory evidence survives the settings 403
		case f.SubjectKind == subjectKindSettingsNote:
			notes++
			if !strings.Contains(f.Title, settingsScope) {
				t.Errorf("403 note must name the missing scope %q: %q", settingsScope, f.Title)
			}
		case f.SubjectKind == subjectKindSetting:
			t.Errorf("a 403 must not produce a fabricated per-setting attestation: %q", f.Title)
		}
	}
	if notes != 1 {
		t.Fatalf("want exactly 1 honest 403 note, got %d", notes)
	}
	if dirFindings == 0 {
		t.Error("the directory evidence must still emit when settings 403s (best-effort)")
	}
}

// TestSettings_Parent404SkippedAndKeysOnce is the regression test for the 404-conflation
// bug: the org list is (parent + linked + linked); the PARENT is not a settings target so
// its /settings 404s. The connector must SKIP it and still attest BOTH linked orgs (never
// stop on the first 404 nor fabricate a "not enabled" note), and emit the hierarchy-wide
// api_keys inventory exactly ONCE across the two successful orgs.
func TestSettings_Parent404SkippedAndKeysOnce(t *testing.T) {
	doer := &routeDoer{handler: func(req *http.Request) (int, string) {
		if req.Method != http.MethodGet {
			t.Errorf("non-GET %s", req.Method)
		}
		p := req.URL.Path
		switch {
		case strings.HasSuffix(p, "/settings"):
			// The parent org (first) is not a settings target => 404; the two linked orgs 200.
			if strings.Contains(p, "org_parent") {
				return http.StatusNotFound, `{"error":"not found"}`
			}
			body := strings.Replace(settingsBody, `"organization_id":"org_a"`, `"organization_id":"`+orgID(p)+`"`, 1)
			return http.StatusOK, body
		case strings.HasSuffix(p, "/roles"):
			return http.StatusOK, `{"data":[],"has_more":false}`
		case strings.HasSuffix(p, "/users"):
			return http.StatusOK, `{"data":[],"has_more":false}`
		case p == groupsPath:
			return http.StatusOK, `{"data":[],"has_more":false}`
		case p == orgsPath:
			return http.StatusOK, `{"data":[{"uuid":"org_parent","name":"Parent"},{"uuid":"org_b","name":"B"},{"uuid":"org_c","name":"C"}]}`
		case p == "/v1/compliance/apps/projects":
			return http.StatusOK, `{"data":[],"has_more":false}`
		default:
			t.Fatalf("unexpected path %q", p)
			return http.StatusInternalServerError, ""
		}
	}}
	s := newDirectorySource(t, doer)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var keyFindings, unavailableNotes int
	attested := map[string]bool{}
	for _, o := range sink.obs {
		f := o.(model.FindingReport)
		switch f.SubjectKind {
		case subjectKindComplianceKey:
			keyFindings++
		case subjectKindSettingsNote:
			unavailableNotes++
		case subjectKindSetting:
			if i := strings.IndexByte(f.SubjectRef, '#'); i > 0 {
				attested[f.SubjectRef[:i]] = true
			}
		}
	}
	if !attested["org_b"] || !attested["org_c"] {
		t.Errorf("both linked orgs must be attested despite the parent's 404; attested=%v", attested)
	}
	if attested["org_parent"] {
		t.Error("the parent (a 404 non-target) must NOT be attested")
	}
	if unavailableNotes != 0 {
		t.Errorf("a per-org 404 with successful linked orgs must NOT emit a false 'not enabled' note, got %d", unavailableNotes)
	}
	if keyFindings != 1 {
		t.Errorf("the hierarchy-wide api_keys inventory must be emitted exactly ONCE across orgs, got %d", keyFindings)
	}
}

// TestSettings_SSOEnforcementAbsence proves the headline blind spot is closed on the
// ABSENCE path too: when an SSO-enforcement control (sso_console_enforced) is omitted, the
// connector emits an explicit not-introspectable finding (never "off"), and ties it back to
// the claude-console unknown-by-api blind spot. (settingsBody omits the sso_* booleans.)
func TestSettings_SSOEnforcementAbsence(t *testing.T) {
	doer := &routeDoer{handler: settingsHandler(t, http.StatusOK, settingsBody)}
	fs := settingsFindings(t, doer)

	got := titleFor(fs, "org_a#sso_console_enforced")
	if got == "" {
		t.Fatal("an omitted SSO-enforcement control must still produce a not-introspectable finding (the blind spot this connector exists to close)")
	}
	low := strings.ToLower(got)
	if !strings.Contains(low, "introspectable") {
		t.Errorf("omitted SSO control must read not-introspectable: %q", got)
	}
	if strings.Contains(low, "enforced:") {
		t.Errorf("an omitted SSO control must NOT be attested to a value (not 'off'): %q", got)
	}
	if !strings.Contains(got, "unknown-by-api") {
		t.Errorf("omitted SSO control should reference the claude-console blind spot: %q", got)
	}
}

// TestSettings_EmptyAPIKeysNoFinding proves an empty api_keys array emits no inventory
// finding (no fabricated "0 keys" row) while the settings rows still attest.
func TestSettings_EmptyAPIKeysNoFinding(t *testing.T) {
	body := strings.Replace(settingsBody,
		`"api_keys":[
    {"id":"ckey_active","name":"siem","is_active":true,"scopes":["read:compliance_activities","read:compliance_org_data"]},
    {"id":"ckey_old","name":"retired","is_active":false,"scopes":["read:compliance_org_data"]}
  ]`,
		`"api_keys":[]`, 1)
	if !strings.Contains(body, `"api_keys":[]`) {
		t.Fatal("test fixture replacement failed — api_keys not emptied")
	}
	doer := &routeDoer{handler: settingsHandler(t, http.StatusOK, body)}
	fs := settingsFindings(t, doer)

	var keyFindings, settingRows int
	for _, f := range fs {
		switch f.SubjectKind {
		case subjectKindComplianceKey:
			keyFindings++
		case subjectKindSetting:
			settingRows++
		}
	}
	if keyFindings != 0 {
		t.Errorf("empty api_keys must emit no inventory finding, got %d", keyFindings)
	}
	if settingRows == 0 {
		t.Error("settings rows must still attest even with empty api_keys")
	}
}

// TestSettings_EmptyFirstKeysDoesNotSuppressLater is the regression test for the
// keysEmitted latch: the FIRST 200-org carries an empty api_keys, a LATER 200-org carries
// the real inventory. The connector must still emit the inventory once (from the later
// org), never letting an empty early org suppress it.
func TestSettings_EmptyFirstKeysDoesNotSuppressLater(t *testing.T) {
	keysBlock := `"api_keys":[
    {"id":"ckey_active","name":"siem","is_active":true,"scopes":["read:compliance_activities","read:compliance_org_data"]},
    {"id":"ckey_old","name":"retired","is_active":false,"scopes":["read:compliance_org_data"]}
  ]`
	emptyFirst := strings.Replace(settingsBody, keysBlock, `"api_keys":[]`, 1)
	if !strings.Contains(emptyFirst, `"api_keys":[]`) {
		t.Fatal("fixture replacement failed")
	}
	doer := &routeDoer{handler: func(req *http.Request) (int, string) {
		p := req.URL.Path
		switch {
		case strings.HasSuffix(p, "/settings"):
			if strings.Contains(p, "org_a") {
				return http.StatusOK, emptyFirst // first org: empty api_keys
			}
			return http.StatusOK, settingsBody // later org: populated inventory
		case strings.HasSuffix(p, "/roles"), strings.HasSuffix(p, "/users"):
			return http.StatusOK, `{"data":[],"has_more":false}`
		case p == groupsPath:
			return http.StatusOK, `{"data":[],"has_more":false}`
		case p == orgsPath:
			return http.StatusOK, `{"data":[{"uuid":"org_a","name":"A"},{"uuid":"org_b","name":"B"}]}`
		case p == "/v1/compliance/apps/projects":
			return http.StatusOK, `{"data":[],"has_more":false}`
		default:
			t.Fatalf("unexpected path %q", p)
			return http.StatusInternalServerError, ""
		}
	}}
	fs := settingsFindings(t, doer)
	var keyFindings int
	var keyTitle string
	for _, f := range fs {
		if f.SubjectKind == subjectKindComplianceKey {
			keyFindings++
			keyTitle = f.Title
		}
	}
	if keyFindings != 1 {
		t.Fatalf("want the inventory emitted exactly once (from the later populated org), got %d", keyFindings)
	}
	if !strings.Contains(keyTitle, "2 total") {
		t.Errorf("emitted inventory must be the populated one, got %q", keyTitle)
	}
}

// orgID extracts the org uuid from a /v1/compliance/organizations/{uuid}/settings path.
func orgID(path string) string {
	const pre = "/v1/compliance/organizations/"
	s := strings.TrimPrefix(path, pre)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestSettings_EndpointNotEnabled404 proves a 404 degrades to one honest "not enabled"
// note (never "no controls"), once, without failing Gather.
func TestSettings_EndpointNotEnabled404(t *testing.T) {
	doer := &routeDoer{handler: settingsHandler(t, http.StatusNotFound, `{"error":"not found"}`)}
	fs := settingsFindings(t, doer)

	var notes int
	for _, f := range fs {
		if f.SubjectKind == subjectKindSettingsNote {
			notes++
			if !strings.Contains(f.Title, "not enabled") {
				t.Errorf("404 note must explain endpoint-not-enabled: %q", f.Title)
			}
		}
	}
	if notes != 1 {
		t.Fatalf("want exactly 1 honest 404 note, got %d", notes)
	}
}

// TestSettings_TransientErrorPropagates proves a 5xx is NOT mislabeled as a capability
// gap: it propagates so the engine retries the whole Gather.
func TestSettings_TransientErrorPropagates(t *testing.T) {
	doer := &routeDoer{handler: settingsHandler(t, http.StatusServiceUnavailable, `{"error":"down"}`)}
	s := newDirectorySource(t, doer)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err == nil {
		t.Fatal("a 503 on the settings endpoint must propagate (engine retries), not be swallowed")
	}
}

// TestSummarizeSettingValue_TypesAndHonesty unit-tests the value interpreter directly,
// including null integers, retention maps with deterministic key order, and the honest
// fallback for an unknown type or a type/value mismatch.
func TestSummarizeSettingValue_TypesAndHonesty(t *testing.T) {
	cases := []struct {
		typ, value, want string
	}{
		{settingTypeBool, `true`, "true"},
		{settingTypeBool, `false`, "false"},
		{settingTypeInteger, `3600`, "3600"},
		{settingTypeInteger, `null`, "no-limit"},
		{settingTypeStringList, `["a"]`, "1 entry"},
		{settingTypeStringList, `["a","b","c"]`, "3 entries"},
		{settingTypeStringList, `[]`, "0 entries"},
		{settingTypeProvision, `"login_only"`, "login_only"},
		{settingTypeRetention, `{"all":{"type":"indefinite"}}`, "all=indefinite"},
		{settingTypeRetention, `{"project":{"type":"indefinite"},"chat":{"type":"fixed","timescale":"month","duration":6}}`, "chat=6month(fixed);project=indefinite"},
		{"mystery_type", `{"k":1}`, "not interpreted"},
		{settingTypeBool, `"not a bool"`, "not interpreted"}, // type/value mismatch is honest, not a crash
	}
	for _, c := range cases {
		got, canonical := summarizeSettingValue(c.typ, []byte(c.value))
		if !strings.Contains(got, c.want) {
			t.Errorf("summarizeSettingValue(%q,%s) = %q, want it to contain %q", c.typ, c.value, got, c.want)
		}
		if canonical == "" {
			t.Errorf("summarizeSettingValue(%q,%s) produced an empty canonical hash preimage", c.typ, c.value)
		}
	}
}

// TestCanonicalJSON_OrderStable proves the hash preimage is stable across object-key
// reorderings (so a re-attestation of the same enforced value hashes identically).
func TestCanonicalJSON_OrderStable(t *testing.T) {
	a := canonicalJSON([]byte(`{"b":2,"a":1}`))
	b := canonicalJSON([]byte(`{"a":1,"b":2}`))
	if a != b {
		t.Fatalf("canonicalJSON must be key-order stable: %q vs %q", a, b)
	}
}

// TestSettings_DenyClosedWithoutKey confirms no Compliance Access Key ⇒ no settings call
// at all (the whole directory+settings ingest is gated on that key).
func TestSettings_DenyClosedWithoutKey(t *testing.T) {
	doer := &routeDoer{handler: func(*http.Request) (int, string) {
		t.Fatal("no compliance_access_key must make NO settings call")
		return 0, ""
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"org_ref": "acme"}}); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("offline (no keys) must emit nothing, got %d", len(sink.obs))
	}
}
