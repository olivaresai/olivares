// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
)

// ProviderBaggageRule permits exact non-secret values for one W3C baggage key
// at one HTTP(S) origin. It grants no destination or inference authorization.
type ProviderBaggageRule struct {
	Origin string   `json:"origin"`
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

const (
	maxProviderBaggageConfig = 16 << 10
	maxProviderBaggageRules  = 32
	maxProviderBaggageValues = 8
	maxProviderBaggageToken  = 64
)

type baggagePolicyState uint8

const (
	baggagePolicyDefaultDeny baggagePolicyState = iota
	baggagePolicyValid
	baggagePolicyInvalid
)

type providerBaggagePolicy struct {
	state  baggagePolicyState
	reason string
	rules  map[string]map[string]map[string]struct{}
}

func parseProviderBaggage(raw string) ([]ProviderBaggageRule, string) {
	if len(raw) > maxProviderBaggageConfig {
		return nil, "over_limit"
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ""
	}
	if raw[0] != '[' {
		return nil, "invalid_json"
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	var rules []ProviderBaggageRule
	if err := d.Decode(&rules); err != nil {
		return nil, "invalid_json"
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, "invalid_json"
	}
	return rules, ""
}

func invalidProviderBaggage(reason string) providerBaggagePolicy {
	return providerBaggagePolicy{state: baggagePolicyInvalid, reason: reason}
}

func compileProviderBaggage(rules []ProviderBaggageRule, parseRejection string) providerBaggagePolicy {
	if parseRejection != "" {
		return invalidProviderBaggage(parseRejection)
	}
	if len(rules) > maxProviderBaggageRules {
		return invalidProviderBaggage("over_limit")
	}
	if len(rules) == 0 {
		return providerBaggagePolicy{state: baggagePolicyDefaultDeny}
	}
	p := providerBaggagePolicy{state: baggagePolicyValid, rules: make(map[string]map[string]map[string]struct{})}
	for _, rule := range rules {
		if len(rule.Key) > maxProviderBaggageToken || len(rule.Values) > maxProviderBaggageValues {
			return invalidProviderBaggage("over_limit")
		}
		if len(rule.Key) == 0 || strings.ContainsRune(rule.Key, '*') || len(rule.Values) == 0 {
			return invalidProviderBaggage("invalid_rule")
		}
		if _, err := baggage.NewMember(rule.Key, "control"); err != nil {
			return invalidProviderBaggage("invalid_rule")
		}
		u, err := url.Parse(rule.Origin)
		if err != nil || strings.ContainsAny(rule.Origin, "?#") || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
			return invalidProviderBaggage("invalid_rule")
		}
		origin, ok := providerOrigin(u)
		if !ok {
			return invalidProviderBaggage("invalid_rule")
		}
		if p.rules[origin] == nil {
			p.rules[origin] = make(map[string]map[string]struct{})
		}
		if _, exists := p.rules[origin][rule.Key]; exists {
			return invalidProviderBaggage("duplicate_rule")
		}
		values := make(map[string]struct{}, len(rule.Values))
		for _, value := range rule.Values {
			if len(value) > maxProviderBaggageToken {
				return invalidProviderBaggage("over_limit")
			}
			if !providerControlValue(value) {
				return invalidProviderBaggage("invalid_rule")
			}
			values[value] = struct{}{}
		}
		p.rules[origin][rule.Key] = values
	}
	return p
}

func providerControlValue(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func providerOrigin(u *url.URL) (string, bool) {
	if u == nil || u.Opaque != "" || u.User != nil {
		return "", false
	}
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname())
	if (scheme != "http" && scheme != "https") || host == "" {
		return "", false
	}
	for _, c := range host {
		if c <= ' ' || c >= 127 || strings.ContainsRune("*\\/#?@%", c) {
			return "", false
		}
	}
	port := 443
	if scheme == "http" {
		port = 80
	}
	if s := u.Port(); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 65535 {
			return "", false
		}
		port = n
	} else if strings.HasSuffix(u.Host, ":") {
		return "", false
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)), true
}

func (p providerBaggagePolicy) filter(ctx context.Context, target *url.URL) baggage.Baggage {
	origin, ok := providerOrigin(target)
	if !ok {
		return baggage.Baggage{}
	}
	rules := p.rules[origin]
	var members []baggage.Member
	for _, member := range baggage.FromContext(ctx).Members() {
		if _, allowed := rules[member.Key()][member.Value()]; !allowed {
			continue
		}
		clean, err := baggage.NewMemberRaw(member.Key(), member.Value())
		if err != nil {
			return baggage.Baggage{}
		}
		members = append(members, clean)
	}
	out, err := baggage.New(members...)
	if err != nil {
		return baggage.Baggage{}
	}
	return out
}

// providerRequest owns the outbound propagation copy. Ingress and internal span
// contexts retain their baggage; the external transport receives only declared
// metadata. This policy applies to AnthropicHTTPClient, not every provider client.
func (p *Provider) providerRequest(ctx context.Context, req *http.Request) *http.Request {
	ctx = baggage.ContextWithBaggage(ctx, p.baggagePolicy.filter(ctx, req.URL))
	out := req.Clone(ctx)
	if out.Header == nil {
		out.Header = make(http.Header)
	}
	for key := range out.Header {
		if strings.EqualFold(key, "baggage") || strings.EqualFold(key, "traceparent") || strings.EqualFold(key, "tracestate") {
			delete(out.Header, key)
		}
	}
	p.propagator.Inject(ctx, propagation.HeaderCarrier(out.Header))
	return out
}
