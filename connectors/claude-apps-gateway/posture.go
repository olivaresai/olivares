// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeappsgateway

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

var dottedVersionRE = regexp.MustCompile(`\b([0-9]+)\.([0-9]+)\.([0-9]+)(?:\.[0-9]+)?\b`)

func postureFindings(gw gatewayYAML, cfg config, ref, probeVersion string, at time.Time) ([]model.FindingReport, error) {
	var out []model.FindingReport
	add := func(kind string, sev model.Severity, title, detail string) {
		out = append(out, finding(kind, sev, ref, title, detail, at))
	}

	if len(gw.Telemetry.ForwardTo) == 0 {
		add("gateway_no_otlp", model.SeverityMedium, "telemetry.forward_to is not configured", "gateway_no_otlp")
	}
	if !hasCatchAllPolicy(gw) {
		add("gateway_unbounded_default", model.SeverityHigh, "no catch-all managed policy", "gateway_unbounded_default")
	}
	if gw.Admin == nil {
		add("gateway_no_spend_limits", model.SeverityMedium, "admin block is absent", "gateway_no_spend_limits")
	} else if gw.Enforcement.FailClosedOnError == nil || !*gw.Enforcement.FailClosedOnError {
		add("gateway_spend_fail_open", model.SeverityLow, "spend enforcement fail_closed_on_error is not true", "gateway_spend_fail_open")
	}
	if len(gw.OIDC.AllowedEmailDomains) == 0 {
		add("gateway_no_domain_gate", model.SeverityMedium, "oidc.allowed_email_domains is not configured", "gateway_no_domain_gate")
	}
	if len(gw.OIDC.AllowedEmailDomains) > 1 {
		add("gateway_single_issuer_multidomain", model.SeverityInfo, "multiple email domains share one OIDC issuer", "gateway_single_issuer_multidomain")
	}
	if gw.ttlHours() > 12 {
		add("gateway_long_session_ttl", model.SeverityMedium, "session.ttl_hours is greater than 12", "gateway_long_session_ttl")
	}
	if !gw.usePKCE() {
		add("gateway_pkce_disabled", model.SeverityLow, "oidc.use_pkce is false", "gateway_pkce_disabled")
	}
	if telemetryCarriesSensitiveSignals(gw) {
		add("gateway_sensitive_signals", model.SeverityLow, "telemetry destination has logs or traces enabled", "gateway_sensitive_signals")
	}
	for _, path := range literalSecretFields(gw) {
		out = append(out, finding("gateway_secret_literal", model.SeverityHigh, ref, "literal secret: "+path, path, at))
	}
	for i, p := range policies(gw) {
		if p.usesSettingsAlias() {
			path := fmt.Sprintf("managed.policies[%d].settings", i)
			out = append(out, finding("gateway_deprecated_settings_alias", model.SeverityInfo, ref, "deprecated settings alias: "+path, path, at))
		}
	}
	if cfg.declaredSettingsPath != "" {
		drift, err := policyDriftFindings(gw, cfg.declaredSettingsPath, ref, at)
		if err != nil {
			return nil, err
		}
		out = append(out, drift...)
	}
	if versionBelowMinApplies(gw, probeVersion) {
		add("gateway_version_below_min", model.SeverityMedium, "gateway version below 2.1.198 with anthropicAws upstream", "gateway_version_below_min|"+normalizeVersion(probeVersion))
	}
	return out, nil
}

func hasCatchAllPolicy(gw gatewayYAML) bool {
	for _, p := range policies(gw) {
		if p.isCatchAll() {
			return true
		}
	}
	return false
}

func telemetryCarriesSensitiveSignals(gw gatewayYAML) bool {
	for _, dst := range gw.Telemetry.ForwardTo {
		if dst.Logs || dst.Traces {
			return true
		}
	}
	return false
}

func literalSecretFields(gw gatewayYAML) []string {
	var out []string
	if isLiteralSecret(gw.OIDC.ClientSecret) {
		out = append(out, "oidc.client_secret")
	}
	for i, v := range gw.Session.JWTSecret {
		if isLiteralSecret(v) {
			out = append(out, fmt.Sprintf("session.jwt_secret[%d]", i))
		}
	}
	if postgresURLHasLiteralPassword(gw.Store.PostgresURL) {
		out = append(out, "store.postgres_url.password")
	}
	if isLiteralSecret(gw.Store.Password) {
		out = append(out, "store.password")
	}
	sort.Strings(out)
	return out
}

func isLiteralSecret(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	return !(strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}"))
}

func postgresURLHasLiteralPassword(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || !isLiteralSecret(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return false
	}
	pass, ok := u.User.Password()
	return ok && isLiteralSecret(pass)
}

func policyDriftFindings(gw gatewayYAML, declaredPath, ref string, at time.Time) ([]model.FindingReport, error) {
	data, err := os.ReadFile(declaredPath) //nolint:gosec // operator-supplied declared policy path
	if err != nil {
		return nil, fmt.Errorf("claude-apps-gateway: read declared_settings_path: %w", err)
	}
	var declared map[string]any
	if err := json.Unmarshal(data, &declared); err != nil {
		return nil, fmt.Errorf("claude-apps-gateway: parse declared_settings_path: %w", err)
	}
	declared = normalizeMap(declared)
	var out []model.FindingReport
	for i, p := range policies(gw) {
		policyDoc := normalizeMap(p.effectiveCLI())
		var divergent []string
		for key, want := range declared {
			got, ok := policyDoc[key]
			if !ok || !jsonEqual(got, want) {
				divergent = append(divergent, key)
			}
		}
		if len(divergent) == 0 {
			continue
		}
		sort.Strings(divergent)
		title := "policy drift keys: " + strings.Join(divergent, ", ")
		detail := fmt.Sprintf("policy=%d|keys=%s", i, strings.Join(divergent, ","))
		out = append(out, finding("policy_drift", model.SeverityMedium, ref, title, detail, at))
	}
	return out, nil
}

func normalizeMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func jsonEqual(a, b any) bool {
	ba, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ba) == string(bb)
}

func versionBelowMinApplies(gw gatewayYAML, version string) bool {
	if normalizeVersion(version) == "" || !hasAnthropicAWS(gw) {
		return false
	}
	return compareDottedVersions(normalizeVersion(version), "2.1.198") < 0
}

func hasAnthropicAWS(gw gatewayYAML) bool {
	for _, u := range gw.Upstreams {
		if strings.EqualFold(strings.TrimSpace(u.Provider), "anthropicAws") || strings.EqualFold(strings.TrimSpace(u.Name), "anthropicAws") {
			return true
		}
	}
	return false
}

func normalizeVersion(s string) string {
	m := dottedVersionRE.FindStringSubmatch(s)
	if len(m) < 4 {
		return ""
	}
	return m[1] + "." + m[2] + "." + m[3]
}

func compareDottedVersions(a, b string) int {
	aa := versionParts(a)
	bb := versionParts(b)
	for i := 0; i < len(aa) && i < len(bb); i++ {
		if aa[i] < bb[i] {
			return -1
		}
		if aa[i] > bb[i] {
			return 1
		}
	}
	return 0
}

func versionParts(s string) [3]int {
	var out [3]int
	parts := strings.Split(normalizeVersion(s), ".")
	for i := 0; i < len(parts) && i < len(out); i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}
