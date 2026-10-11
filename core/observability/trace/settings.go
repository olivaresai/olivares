// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"errors"
	"math"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/core/envconfig"
)

// Settings contains administrator choices. Provider baggage remains an explicit host policy.
type Settings struct {
	Enabled     bool     `json:"enabled"`
	Endpoint    string   `json:"endpoint"`
	Protocol    Protocol `json:"protocol"`
	Insecure    bool     `json:"insecure"`
	SampleRatio float64  `json:"sample_ratio"`
	ServiceName string   `json:"service_name"`
	GenAICompat bool     `json:"genai_compat"`
}

func DefaultSettings() Settings {
	return Settings{Protocol: ProtocolGRPC, SampleRatio: 1, ServiceName: defaultServiceName}
}

func (s Settings) Validate() error {
	if s.Protocol != ProtocolGRPC && s.Protocol != ProtocolHTTP {
		return errors.New("choose grpc or http/protobuf")
	}
	if math.IsNaN(s.SampleRatio) || math.IsInf(s.SampleRatio, 0) || s.SampleRatio < 0 || s.SampleRatio > 1 {
		return errors.New("sample ratio must be between 0 and 1")
	}
	if strings.TrimSpace(s.ServiceName) == "" || len(s.ServiceName) > 256 || strings.ContainsAny(s.ServiceName, "\r\n\x00") {
		return errors.New("service name must contain 1 to 256 printable characters")
	}
	if s.Endpoint == "" {
		if s.Enabled {
			return errors.New("enter the collector endpoint before enabling tracing")
		}
		return nil
	}
	if len(s.Endpoint) > 4096 || strings.TrimSpace(s.Endpoint) != s.Endpoint || strings.ContainsAny(s.Endpoint, "\r\n\x00") {
		return errors.New("invalid collector endpoint")
	}
	endpoint := s.Endpoint
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("collector endpoint must be a host or an HTTP(S) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("collector endpoint must not contain credentials, a query or a fragment")
	}
	if u.Scheme == "http" && !s.Insecure {
		return errors.New("enable plaintext only for a trusted collector network, or use HTTPS")
	}
	return nil
}

// Resolve keeps published environment inputs as explicit overrides of saved choices.
// With no saved choices the historical FromEnv behavior is unchanged.
func (s Settings) Resolve(version string) (Config, []string) {
	env := FromEnv(version)
	cfg := env
	cfg.Enabled, cfg.Endpoint, cfg.Protocol = s.Enabled, s.Endpoint, s.Protocol
	cfg.Insecure, cfg.SampleRatio = s.Insecure, s.SampleRatio
	cfg.ServiceName, cfg.GenAICompat = s.ServiceName, s.GenAICompat
	var overrides []string
	present := func(keys ...string) bool {
		for _, key := range keys {
			if strings.TrimSpace(envconfig.Get(key)) != "" {
				overrides = append(overrides, key)
				return true
			}
		}
		return false
	}
	endpoint := present("OLIVARES_OTEL_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT")
	enabled := present("OLIVARES_OTEL_ENABLED")
	if endpoint {
		cfg.Endpoint = env.Endpoint
	}
	if enabled || endpoint {
		cfg.Enabled = env.Enabled
	}
	if present("OLIVARES_OTEL_PROTOCOL", "OTEL_EXPORTER_OTLP_PROTOCOL") {
		cfg.Protocol = env.Protocol
	}
	if present("OLIVARES_OTEL_INSECURE") {
		cfg.Insecure = env.Insecure
	}
	if present("OLIVARES_OTEL_SAMPLE_RATIO") {
		cfg.SampleRatio = env.SampleRatio
	}
	if present("OLIVARES_OTEL_SERVICE_NAME") {
		cfg.ServiceName = env.ServiceName
	}
	if present("OLIVARES_OTEL_GENAI_COMPAT") {
		cfg.GenAICompat = env.GenAICompat
	}
	// Exporter transport/credential inputs remain handled by the installed OTel SDK;
	// the standard semconv opt-in is read from the environment by FromEnv (no saved
	// form), so its presence is only reported as a recognized override.
	for _, key := range []string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_INSECURE", "OTEL_EXPORTER_OTLP_CERTIFICATE", "OTEL_EXPORTER_OTLP_HEADERS", "OTEL_SEMCONV_STABILITY_OPT_IN", "OLIVARES_OTEL_PROVIDER_BAGGAGE_ALLOWLIST"} {
		present(key)
	}
	return cfg, overrides
}

// VisibleSettings never exposes credentials from a legacy environment endpoint.
func VisibleSettings(c Config) Settings {
	endpoint := c.Endpoint
	if strings.ContainsAny(endpoint, "@?#") {
		endpoint = "<redacted>"
	}
	return Settings{Enabled: c.Enabled, Endpoint: endpoint, Protocol: c.Protocol, Insecure: c.Insecure, SampleRatio: c.SampleRatio, ServiceName: c.ServiceName, GenAICompat: c.GenAICompat}
}
