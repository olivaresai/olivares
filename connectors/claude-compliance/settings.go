// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file is the EFFECTIVE-SETTINGS attestation arm of the Compliance directory
// surface (CLA gap #5). It is a read-only, minimal-data poll of
// GET /v1/compliance/organizations/{org_uuid}/settings that turns the controls Anthropic
// ENFORCES at the org level — data retention, content redaction, the IP allowlist, the
// SSO/SCIM provisioning mode, code-execution network egress, and the rest — into
// continuous, ledger-sealed POSTURE evidence an auditor can attest against a documented
// baseline WITHOUT administrator Console access (exactly what ISO 42006 / EU AI Act
// continuous-control attestation asks for).
//
// It is the read that RESOLVES the claude-console blind spot: that connector can only
// emit `sso_enforcement=unknown-by-api` because the public Admin API exposes no
// enforced-state surface. This endpoint now rides under read:compliance_org_data on the
// SAME Compliance Access Key the directory uses; the dedicated
// read:compliance_org_settings scope was RETIRED (~2026-06-30; VERIFIED 2026-07-03).
// It exposes the resolved enforced state, so "unknown-by-api" becomes either a concrete
// attested value or an HONEST "not introspectable" — never a fabricated "off". It
// complements honest note that the code-execution container egress boundary is
// Anthropic's, by ATTESTING whether that org has
// `code_execution_network_egress_enabled`.
//
// HONESTY BY CONSTRUCTION (verified against Anthropic primary docs,
// platform.claude.com/docs/en/api/compliance/organizations/settings/retrieve):
//   - The response is a LIST of typed rows; WHICH rows appear varies by org. A setting an
//     org's admins cannot change (Anthropic-policy-controlled or unavailable) is OMITTED.
//     Anthropic states — and this connector enforces — that a MISSING row means
//     "not controllable by this org's admins", NEVER "off". So an absent NAMED control
//     emits an explicit not-introspectable posture finding, not a false "off".
//   - The endpoint is enabled per-parent-org separately and accepts only a Compliance
//     Access Key (sk-ant-api01-) carrying read:compliance_org_data; the dedicated
//     read:compliance_org_settings scope was RETIRED (~2026-06-30; VERIFIED 2026-07-03).
//     An Admin key is rejected. A 403 (scope/key wrong) or a 404 (endpoint not enabled / org not a
//     target) degrades to ONE honest posture note and never fabricates a result; a
//     transport/5xx error propagates so the engine retries rather than mislabeling a gap.
//   - Minimal-data (docs/SECURITY-HARDENING.md): the enforced VALUE is non-sensitive CONFIGURATION (no
//     PII, secret, or content), so the human-readable enforced state rides the Title for
//     the auditor and the full structural row is sealed into the one-way DetailHash for
//     tamper-evidence. No user email, key secret, or chat content is ever read.
//
// CONTRACT FOR CONSUMERS (claude-console / the console / the compliance module). Every
// emission here is Kind "posture", distinguished by SubjectKind:
//   - claude_org_setting        — one enforced control row. SubjectRef = "<org_uuid>#<name>".
//     Title "… enforced: <value>" attests a present control;
//     Title "… not controllable/introspectable …" marks an
//     OMITTED named control (never "off"). Severity Info, or Low
//     for a security-weak enforced state. An SSO control's Title
//     declares it RESOLVES claude-console's sso_enforcement=
//     unknown-by-api — once sso_provisioning_mode/sso_*_enforced
//     read here, that connector's blind-spot finding is answered.
//   - claude_compliance_settings — the single honest note when the endpoint is unreadable
//     (missing read:compliance_org_data scope, or not enabled).
//   - claude_compliance_key      — the (secrets-free) compliance-key inventory of the hierarchy.
package claudecompliance

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	// settingsScope is the scope the effective-settings endpoint now requires on the
	// Compliance Access Key. The dedicated read:compliance_org_settings scope was
	// RETIRED (~2026-06-30); effective settings now ride under read:compliance_org_data
	// (VERIFIED 2026-07-03). Named so an honest note can tell the operator exactly
	// which current scope to grant.
	settingsScope = "read:compliance_org_data"

	// findingKindSettings is the wire Kind of an effective-settings attestation. It shares
	// the "posture" bucket with the coverage finding (both are posture evidence, not
	// activity or directory inventory) and is distinguished by SubjectKind, so it never
	// pollutes the external_activity / directory evidence counts module XIII keys on.
	findingKindSettings = "posture"

	// Setting-evidence SubjectKinds.
	subjectKindSetting       = "claude_org_setting"         // one attested or absent control row
	subjectKindSettingsNote  = "claude_compliance_settings" // the per-parent honest-degradation note
	subjectKindComplianceKey = "claude_compliance_key"      // the (secrets-free) compliance-key inventory

	// Anthropic's typed setting-row value kinds (the `type` discriminator).
	settingTypeBool       = "boolean"
	settingTypeInteger    = "integer"
	settingTypeStringList = "string_list"
	settingTypeProvision  = "provisioning_mode"
	settingTypeRetention  = "data_retention"
)

// attestedControls is the set of org-level enforced controls the connector explicitly
// attests the PRESENCE of: the five families gap #5 names and the claude-console blind
// spot asks about. When the settings response carries the row, its enforced value is
// attested; when the row is ABSENT, the connector emits a "not controllable /
// not introspectable" finding — because Anthropic OMITS a row the org's admins cannot
// change — NEVER a fake "off". Other rows the response carries are still attested
// generically; only these five get the explicit absence finding, because they are the
// controls an auditor names by family.
var attestedControls = []struct {
	row    string // the exact Anthropic setting `name`
	family string // the gap #5 control family it belongs to
}{
	{"data_retention_periods", "data_retention"},
	{"content_redaction_enabled", "content_redaction"},
	{"ip_allowlist_enabled", "ip_allowlist"},
	{"sso_provisioning_mode", "sso_provisioning_mode"},
	{"code_execution_network_egress_enabled", "code_execution_network_egress"},
	// The SSO-ENFORCEMENT booleans ARE the claude-console sso_enforcement=unknown-by-api
	// blind spot (sso_provisioning_mode above is only the SCIM provisioning half). They
	// are listed so an OMITTED enforcement row yields an explicit not-introspectable
	// finding — the honest-absence guarantee must cover the very control this connector
	// exists to resolve, not only the present-row path.
	{"sso_enabled", "sso_enforcement"},
	{"sso_claude_ai_enforced", "sso_enforcement"},
	{"sso_console_enforced", "sso_enforcement"},
}

// orgSettingsResponse is the minimal projection of GET /v1/compliance/organizations/
// {org_uuid}/settings. `settings` is a list of typed rows; `api_keys` is the
// (secrets-free) compliance-key inventory for the hierarchy; `organization_id` is the
// bare org UUID. The SENSITIVE/irrelevant fields Anthropic may add are not mapped.
type orgSettingsResponse struct {
	Type           string             `json:"type"`
	OrganizationID string             `json:"organization_id"`
	Settings       []settingRow       `json:"settings"`
	APIKeys        []complianceAPIKey `json:"api_keys"`
}

// settingRow is one enforced-setting row. Value's shape is keyed by Type; it is kept RAW
// so an unrecognized type is reported honestly ("present, not interpreted") rather than
// mis-decoded or guessed.
type settingRow struct {
	Name  string          `json:"name"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// complianceAPIKey is one compliance key in the org hierarchy. Anthropic never returns a
// key secret; only the non-sensitive governance fields are modeled.
type complianceAPIKey struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	IsActive bool     `json:"is_active"`
	Scopes   []string `json:"scopes"`
}

// gatherSettings attests the effective org-level enforced controls for each linked org
// the directory listed (best-effort on read:compliance_org_data; the dedicated
// read:compliance_org_settings scope was RETIRED ~2026-06-30, VERIFIED 2026-07-03).
// It runs inside the directory ingest because a settings poll is per-linked-org, listing
// those orgs already needs read:compliance_org_data, and it reuses the SAME (bounded) org
// list and Compliance Access Key client — so it adds no extra org listing. Degradation is
// status-aware and honest: a 403 is KEY-WIDE (scope/key wrong) so it emits ONE note and
// stops; a 404 is PER-ORG (the parent and any non-target org 404, yet appear in the org
// list) so it SKIPS that org and keeps attesting the rest, emitting a single "not enabled"
// note ONLY if NO org could be attested (never fabricating a parent-wide gap from one 404);
// a transport/5xx error propagates (the engine retries) rather than being mislabeled.
func (s *Source) gatherSettings(ctx context.Context, sink sdk.Sink, orgs []complianceOrg, at time.Time) error {
	keysEmitted := false
	attestedAny := false
	saw404 := false
	for _, o := range orgs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if o.UUID == "" {
			continue
		}
		var resp orgSettingsResponse
		err := s.cakClient.GetJSON(ctx, orgsPath+"/"+url.PathEscape(o.UUID)+"/settings", url.Values{}, &resp)
		if err != nil {
			switch {
			case isStatus(err, 403):
				// 403 is KEY-WIDE: the Compliance Access Key lacks read:compliance_org_data
				// for effective settings (or an Admin key was supplied — the settings endpoint
				// rejects sk-ant-admin01-). The dedicated read:compliance_org_settings scope
				// was RETIRED (~2026-06-30; VERIFIED 2026-07-03). It applies to every org, so
				// say so once and stop; the directory evidence already emitted stands.
				return s.emitSettingsUnavailable(ctx, sink,
					"Claude effective-settings attestation unavailable: the Compliance Access Key is missing the "+settingsScope+" scope (or is an Admin key, which this endpoint rejects)",
					"reason=scope_missing;scope="+settingsScope, at)
			case isStatus(err, 404):
				// A 404 is PER-ORG, NOT parent-wide: the endpoint rejects a non-target org —
				// notably the PARENT itself, which is not a settings target yet appears in the
				// organizations list (parent + linked). So SKIP this org and keep attesting the
				// rest; never let one org's 404 fabricate a parent-wide "not enabled" posture or
				// silently drop the genuinely-linked orgs that follow.
				saw404 = true
				continue
			default:
				return err // transport/5xx — propagate for retry, never mislabel as a gap
			}
		}
		if !keysEmitted && len(resp.APIKeys) > 0 {
			// The api_keys inventory is hierarchy-scoped (the API returns the keys configured
			// for the org hierarchy), so emit it once — on the first org that actually carries
			// it. Guarding on len>0 (not just !keysEmitted) means an empty api_keys on an
			// early org never suppresses a later org's real inventory: we do not assume every
			// org echoes an identical array, we wait for the first non-empty one.
			if err := s.emitComplianceKeys(ctx, sink, resp.APIKeys, at); err != nil {
				return err
			}
			keysEmitted = true
		}
		if err := s.emitOrgSettings(ctx, sink, o.UUID, resp, at); err != nil {
			return err
		}
		attestedAny = true
	}
	// Only when NO org could be attested AND at least one returned 404 is the endpoint
	// genuinely unreadable for this parent (or no linked org is a settings target) — emit
	// the single honest note then. If any org WAS attested, the 404s were per-org (e.g. the
	// parent), so a "not enabled" note would be false; emit nothing.
	if !attestedAny && saw404 {
		return s.emitSettingsUnavailable(ctx, sink,
			"Claude effective-settings attestation unavailable: the endpoint is not enabled for this parent organization, or no linked organization is a settings target (contact your Anthropic representative)",
			"reason=endpoint_not_enabled", at)
	}
	return nil
}

// emitOrgSettings maps each enforced setting row to a posture finding sealing the row,
// then — for each NAMED control the response did NOT carry — emits an explicit
// "not controllable / not introspectable" finding (Anthropic omits a row the org's admins
// cannot change). A present row's enforced value is attested; an absent named control is
// NEVER reported as "off".
func (s *Source) emitOrgSettings(ctx context.Context, sink sdk.Sink, orgUUID string, resp orgSettingsResponse, at time.Time) error {
	seen := make(map[string]bool, len(resp.Settings))
	for _, row := range resp.Settings {
		if row.Name == "" {
			continue
		}
		seen[row.Name] = true
		if err := sink.Emit(ctx, s.settingFinding(orgUUID, row, at)); err != nil {
			return err
		}
	}
	for _, c := range attestedControls {
		if seen[c.row] {
			continue
		}
		if err := sink.Emit(ctx, s.settingAbsentFinding(orgUUID, c.row, c.family, at)); err != nil {
			return err
		}
	}
	return nil
}

// settingFinding seals one enforced setting row as posture evidence. The enforced state
// is summarized — legibly, because it is non-sensitive configuration the auditor must be
// able to read — into the Title; the full (org|name|type|canonical-value) tuple is folded
// into the one-way DetailHash for tamper-evidence. Severity is CALIBRATED (settingPosture):
// the connector emits EVIDENCE, not alerts, so most controls stay Info and only a few
// unambiguously security-WEAKENING enforced states are raised to Low — a graded signal an
// auditor thresholds against a baseline, never a spurious alert (the enforced value is
// always in the Title so a downstream rule can override). An SSO control additionally
// declares it RESOLVES the claude-console sso_enforcement=unknown-by-api blind spot.
func (s *Source) settingFinding(orgUUID string, row settingRow, at time.Time) model.FindingReport {
	summary, canonical := summarizeSettingValue(row.Type, row.Value)
	title := "Claude org control [" + row.Name + "] enforced: " + summary
	if isSSOControl(row.Name) {
		title += " — resolves the claude-console sso_enforcement=unknown-by-api blind spot"
	}
	return model.FindingReport{
		Kind:        findingKindSettings,
		Severity:    settingPosture(row.Name, summary),
		SubjectKind: subjectKindSetting,
		SubjectRef:  orgUUID + "#" + row.Name,
		Title:       title,
		DetailHash:  redact.Hash(s.orgRef + "|setting|" + orgUUID + "|" + row.Name + "|" + nonEmpty(row.Type, "?") + "|" + canonical),
		OccurredAt:  at,
	}
}

// weakWhen maps a control to the enforced-value summary that is the security-WEAKER
// posture (a missing entry means the control is never graded above Info). The grading is
// deliberately conservative — Low, never Medium/High — because this connector attests
// evidence; a downstream compliance rule decides whether a Low control breaches a given
// org's documented baseline (some orgs legitimately run without, e.g., an IP allowlist).
var weakWhen = map[string]string{
	"content_redaction_enabled":             "false",      // redaction off
	"ip_allowlist_enabled":                  "false",      // no network restriction
	"sso_enabled":                           "false",      // SSO not enabled
	"sso_claude_ai_enforced":                "false",      // SSO not enforced on claude.ai
	"sso_console_enforced":                  "false",      // SSO not enforced on the Console
	"code_execution_network_egress_enabled": "true",       // code-exec containers may reach the internet (egress on)
	"sso_provisioning_mode":                 "login_only", // no JIT/SCIM provisioning enforced
}

// settingPosture grades an enforced control's value: Low for a recognized security-weak
// enforced state, Info otherwise. Conservative by design (see weakWhen).
func settingPosture(name, summary string) model.Severity {
	if weak, ok := weakWhen[name]; ok && summary == weak {
		return model.SeverityLow
	}
	return model.SeverityInfo
}

// isSSOControl reports whether name is one of the SSO enforcement/provisioning controls
// that, when attested here, resolve the claude-console sso_enforcement=unknown-by-api
// blind spot (claude-console.go:152) — the public Admin API cannot read these; this
// effective-settings endpoint can.
func isSSOControl(name string) bool {
	switch name {
	case "sso_enabled", "sso_claude_ai_enforced", "sso_console_enforced", "sso_provisioning_mode":
		return true
	default:
		return false
	}
}

// settingAbsentFinding records that a NAMED org-level control was OMITTED from the
// effective-settings response. Per Anthropic's contract a missing row means the control is
// "not controllable by this org's administrators" (Anthropic-policy-controlled or
// unavailable) — so it is reported as NOT INTROSPECTABLE, explicitly NOT as "off".
func (s *Source) settingAbsentFinding(orgUUID, row, family string, at time.Time) model.FindingReport {
	title := "Claude org control [" + row + "] not controllable/introspectable by org admin (omitted by Anthropic policy or unavailable) — NOT 'off'"
	if isSSOControl(row) {
		// Closes the loop honestly: claude-console can only say unknown-by-api; here we
		// confirm the enforced state is genuinely not introspectable (Anthropic-controlled),
		// which is still NOT the same as "off".
		title += " — claude-console sso_enforcement=unknown-by-api confirmed not introspectable here (Anthropic-controlled), not disabled"
	}
	return model.FindingReport{
		Kind:        findingKindSettings,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectKindSetting,
		SubjectRef:  orgUUID + "#" + row,
		Title:       title,
		DetailHash:  redact.Hash(s.orgRef + "|setting-absent|" + orgUUID + "|" + row + "|" + family),
		OccurredAt:  at,
	}
}

// emitSettingsUnavailable emits the single honest-degradation posture note when the
// effective-settings endpoint cannot be read (missing scope, or not enabled). It is Info —
// the operator may simply not have granted the scope — and it carries the precise reason
// so the gap is actionable, never a fabricated posture.
func (s *Source) emitSettingsUnavailable(ctx context.Context, sink sdk.Sink, title, detail string, at time.Time) error {
	return sink.Emit(ctx, model.FindingReport{
		Kind:        findingKindSettings,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectKindSettingsNote,
		SubjectRef:  s.orgRef,
		Title:       title,
		DetailHash:  redact.Hash(s.orgRef + "|settings-unavailable|" + detail),
		OccurredAt:  at,
	})
}

// emitComplianceKeys attests the (secrets-free) inventory of Compliance API keys in the
// org hierarchy the settings response returns — how many exist, how many are active vs
// deactivated (a deactivated key is listed for audit visibility) — as a single
// minimal-data posture finding. It surfaces WHO can read compliance data (a
// least-privilege attestation), with the per-key ids and the union of scopes sealed into
// the one-way DetailHash. Nothing is emitted when the response carries no keys.
func (s *Source) emitComplianceKeys(ctx context.Context, sink sdk.Sink, keys []complianceAPIKey, at time.Time) error {
	if len(keys) == 0 {
		return nil
	}
	active := 0
	scopeSet := map[string]struct{}{}
	var detail strings.Builder
	for _, k := range keys {
		if k.IsActive {
			active++
		}
		for _, sc := range k.Scopes {
			scopeSet[sc] = struct{}{}
		}
		detail.WriteString(k.ID + ":" + strconv.FormatBool(k.IsActive) + ";")
	}
	scopes := make([]string, 0, len(scopeSet))
	for sc := range scopeSet {
		scopes = append(scopes, sc)
	}
	sort.Strings(scopes)
	title := "Compliance API keys in org hierarchy: " + strconv.Itoa(len(keys)) + " total, " +
		strconv.Itoa(active) + " active, " + strconv.Itoa(len(keys)-active) + " deactivated (listed for audit)"
	return sink.Emit(ctx, model.FindingReport{
		Kind:        findingKindSettings,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectKindComplianceKey,
		SubjectRef:  s.orgRef,
		Title:       title,
		DetailHash:  redact.Hash(s.orgRef + "|compliance-keys|scopes=" + strings.Join(scopes, ",") + "|" + detail.String()),
		OccurredAt:  at,
	})
}

// summarizeSettingValue interprets a typed setting value into (a) a legible human summary
// for the Title and (b) a canonical string for the tamper-evidence hash. Known types are
// decoded; an UNKNOWN type — or a value that does not match its declared type — is
// reported honestly as "present (type=…, not interpreted)", forward-compatible and never a
// guessed meaning (mirrors the taxonomy's CategoryOther). The canonical form is the
// re-marshaled JSON (object keys sorted by encoding/json) so a re-attestation of the same
// enforced value hashes identically regardless of field order.
func summarizeSettingValue(typ string, raw json.RawMessage) (summary, canonical string) {
	canonical = canonicalJSON(raw)
	switch typ {
	case settingTypeBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil {
			return strconv.FormatBool(b), canonical
		}
	case settingTypeInteger:
		// null => no limit in force (Anthropic semantics for account_session_duration_seconds).
		if isJSONNull(raw) {
			return "no-limit", canonical
		}
		var n json.Number
		if err := json.Unmarshal(raw, &n); err == nil {
			return n.String(), canonical
		}
	case settingTypeStringList:
		var list []string
		if err := json.Unmarshal(raw, &list); err == nil {
			return strconv.Itoa(len(list)) + " entr" + plural(len(list), "y", "ies"), canonical
		}
	case settingTypeProvision:
		var mode string
		if err := json.Unmarshal(raw, &mode); err == nil && mode != "" {
			return mode, canonical
		}
	case settingTypeRetention:
		if sum := summarizeRetention(raw); sum != "" {
			return sum, canonical
		}
	}
	return "present (type=" + nonEmpty(typ, "?") + ", value not interpreted)", canonical
}

// retentionEntry is one data-type's retention rule: a fixed window or indefinite.
type retentionEntry struct {
	Type      string `json:"type"`      // "fixed" | "indefinite"
	Duration  int    `json:"duration"`  // present for "fixed"
	Timescale string `json:"timescale"` // "day" | "month" for "fixed"
}

// summarizeRetention renders the data_retention_periods map (keyed by data type) into a
// compact, deterministic summary like "chat=90day(fixed);project=indefinite". A key of
// "all" is exclusive (it covers every data type). Keys are sorted so the summary is stable
// across reads. An empty/unparseable map yields "" so the caller falls back to the honest
// not-interpreted form.
func summarizeRetention(raw json.RawMessage) string {
	var m map[string]retentionEntry
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		e := m[k]
		switch e.Type {
		case "fixed":
			parts = append(parts, k+"="+strconv.Itoa(e.Duration)+nonEmpty(e.Timescale, "?")+"(fixed)")
		case "indefinite":
			parts = append(parts, k+"=indefinite")
		default:
			parts = append(parts, k+"="+nonEmpty(e.Type, "?"))
		}
	}
	return strings.Join(parts, ";")
}

// canonicalJSON returns a deterministic, key-sorted re-encoding of raw (encoding/json
// sorts object keys on marshal), so the same enforced value hashes identically on every
// re-attestation regardless of the order Anthropic serialized its fields. Empty or
// invalid raw falls back to the trimmed original (still deterministic).
func canonicalJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return strings.TrimSpace(string(raw))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(b)
}

// isJSONNull reports whether raw is the JSON null literal.
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// plural returns one when n == 1, else many.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// isStatus reports whether err is a modelprovider non-2xx response carrying exactly the
// given HTTP status. It reads the typed *modelprovider.APIError rather than substring-
// matching an error string that embeds a server-controlled body excerpt — important here
// because 403 and 404 route to materially different posture outcomes. It never matches a
// transport error, so a 5xx/network failure still propagates for the engine to retry
// rather than being mislabeled as a permanent capability gap.
func isStatus(err error, code int) bool {
	var ae *modelprovider.APIError
	return errors.As(err, &ae) && ae.Status == code
}
