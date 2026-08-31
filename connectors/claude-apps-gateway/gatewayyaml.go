// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeappsgateway

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type gatewayYAML struct {
	Listen struct {
		PublicURL      string     `yaml:"public_url"`
		TLS            any        `yaml:"tls"`
		TrustedProxies stringList `yaml:"trusted_proxies"`
	} `yaml:"listen"`
	OIDC struct {
		Issuer              string     `yaml:"issuer"`
		AllowedEmailDomains stringList `yaml:"allowed_email_domains"`
		AllowedGroups       stringList `yaml:"allowed_groups"`
		UsePKCE             *bool      `yaml:"use_pkce"`
		ClientSecret        string     `yaml:"client_secret"`
	} `yaml:"oidc"`
	Session struct {
		TTLHours  *int       `yaml:"ttl_hours"`
		JWTSecret stringList `yaml:"jwt_secret"`
	} `yaml:"session"`
	Store struct {
		PostgresURL string `yaml:"postgres_url"`
		Password    string `yaml:"password"`
	} `yaml:"store"`
	Admin                  *adminBlock      `yaml:"admin"`
	Enforcement            enforcement      `yaml:"enforcement"`
	Upstreams              []upstream       `yaml:"upstreams"`
	AutoIncludeBuiltinMode *bool            `yaml:"auto_include_builtin_models"`
	Models                 []gatewayModel   `yaml:"models"`
	Managed                *managedBlock    `yaml:"managed"`
	Telemetry              telemetrySection `yaml:"telemetry"`
}

type adminBlock struct {
	WriteKeys   any        `yaml:"write_keys"`
	ReadKeys    any        `yaml:"read_keys"`
	AdminGroups stringList `yaml:"admin_groups"`
}

type enforcement struct {
	FailClosedOnError *bool `yaml:"fail_closed_on_error"`
}

type upstream struct {
	Provider string `yaml:"provider"`
	Region   string `yaml:"region"`
	Name     string `yaml:"name"`
}

type gatewayModel struct {
	ID string `yaml:"id"`
}

type managedBlock struct {
	Policies []managedPolicy `yaml:"policies"`
}

type managedPolicy struct {
	Match    policyMatch    `yaml:"match"`
	CLI      map[string]any `yaml:"cli"`
	Settings map[string]any `yaml:"settings"`
}

type policyMatch struct {
	Groups      stringList `yaml:"groups"`
	EmailDomain string     `yaml:"email_domain"`
}

type telemetrySection struct {
	ForwardTo []telemetryDestination `yaml:"forward_to"`
}

type telemetryDestination struct {
	URL     string `yaml:"url"`
	Metrics bool   `yaml:"metrics"`
	Logs    bool   `yaml:"logs"`
	Traces  bool   `yaml:"traces"`
}

type stringList []string

func (s *stringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value == "" {
			*s = nil
			return nil
		}
		*s = []string{value.Value}
		return nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(value.Content))
		for _, n := range value.Content {
			if n.Kind != yaml.ScalarNode {
				return fmt.Errorf("expected scalar string")
			}
			if n.Value != "" {
				out = append(out, n.Value)
			}
		}
		*s = out
		return nil
	case 0:
		return nil
	default:
		return fmt.Errorf("expected string or string list")
	}
}

func parseGatewayYAML(data []byte) (gatewayYAML, error) {
	var gw gatewayYAML
	if err := yaml.Unmarshal(data, &gw); err != nil {
		return gatewayYAML{}, err
	}
	return gw, nil
}

func (g gatewayYAML) usePKCE() bool {
	if g.OIDC.UsePKCE == nil {
		return true
	}
	return *g.OIDC.UsePKCE
}

func (g gatewayYAML) ttlHours() int {
	if g.Session.TTLHours == nil {
		return 1
	}
	return *g.Session.TTLHours
}

func (p managedPolicy) effectiveCLI() map[string]any {
	if p.CLI != nil {
		return p.CLI
	}
	return p.Settings
}

func (p managedPolicy) usesSettingsAlias() bool {
	return p.Settings != nil
}

func (p managedPolicy) isCatchAll() bool {
	return len(p.Match.Groups) == 0 && p.Match.EmailDomain == ""
}
