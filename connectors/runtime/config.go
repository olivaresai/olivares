// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.runtime"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgEnableLinux = "enable_linux"
	cfgProcRoot    = "proc_root"
	cfgHost        = "host"
	cfgAIPatterns  = "ai_patterns"

	cfgEnableDocker = "enable_docker"
	cfgDockerSocket = "docker_socket"

	cfgEnableK8s             = "enable_k8s"
	cfgK8sAPIServer          = "k8s_api_server"
	cfgK8sToken              = "k8s_token"
	cfgK8sTokenFile          = "k8s_token_file"
	cfgK8sCAFile             = "k8s_ca_file"
	cfgK8sNamespaces         = "k8s_namespaces"
	cfgK8sInsecureSkipVerify = "k8s_insecure_skip_verify"

	cfgTimeout = "timeout"
)

// Defaults for the config fields.
const (
	defaultProcRoot     = "/proc"
	defaultDockerSocket = "/var/run/docker.sock"
	defaultK8sTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec // path, not a secret
	defaultK8sCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	defaultTimeout      = 30 * time.Second

	// defaultAIPatterns lists the case-insensitive substrings that mark a process,
	// container image or pod image as an AI workload. The defaults name concrete AI
	// tooling rather than generic words: the bare token "agent" is deliberately
	// EXCLUDED because, as a substring, it matches ubiquitous non-AI system daemons
	// (ssh-agent, gpg-agent, polkit-agent, gnome-keyring agents), which would flood
	// the inventory with false positives. Operators who want catch-all discovery can
	// add "agent" (or any token) via the ai_patterns setting — matching is substring.
	defaultAIPatterns = "claude,claude-code,mcp,modelcontextprotocol,ollama,vllm,llama.cpp,langgraph,crewai,autogen,llama_index,pydantic,strands,openai,anthropic"
)

// config is the resolved connector configuration. Secret values (k8s_token) are
// held in memory only and never logged or emitted.
type config struct {
	enableLinux bool
	procRoot    string
	host        string
	aiPatterns  []string

	enableDocker bool
	dockerSocket string

	enableK8s             bool
	k8sAPIServer          string
	k8sToken              string // secret, in-memory only
	k8sTokenFile          string
	k8sCAFile             string
	k8sNamespaces         []string
	k8sInsecureSkipVerify bool

	timeout time.Duration
}

// descriptor is the connector's stable self-description. The k8s bearer token is
// declared Secret:true so the UI masks it and logs never print it; its value is
// passed by reference and read only in Open.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Runtime inventory",
		Description: "Read-only discovery of where AI workloads run (Linux procfs, Docker daemon, Kubernetes API); emits containment edges + health findings, minimal data.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgEnableLinux, Type: sdk.FieldBool, Default: "true", Description: "discover AI processes from a procfs"},
			{Key: cfgProcRoot, Type: sdk.FieldString, Default: defaultProcRoot, Description: "procfs root to walk"},
			{Key: cfgHost, Type: sdk.FieldString, Description: "host name used as the process/container origin (defaults to os.Hostname)"},
			{Key: cfgAIPatterns, Type: sdk.FieldString, Default: defaultAIPatterns, Description: "comma-separated case-insensitive substrings that mark an AI workload"},

			{Key: cfgEnableDocker, Type: sdk.FieldBool, Default: "false", Description: "discover containers/images from the Docker daemon (OFF by default: read access to docker.sock is root-equivalent — opt in deliberately, ideally via a read-only/GET-allowlisted socket proxy, docs/08 §2)"},
			{Key: cfgDockerSocket, Type: sdk.FieldString, Default: defaultDockerSocket, Description: "path to the Docker daemon unix socket (read-only API; GET-only)"},

			{Key: cfgEnableK8s, Type: sdk.FieldBool, Default: "true", Description: "discover nodes/pods/deployments from the Kubernetes API"},
			{Key: cfgK8sAPIServer, Type: sdk.FieldString, Description: "Kubernetes API server base URL (empty ⇒ in-cluster autodetect)"},
			{Key: cfgK8sToken, Type: sdk.FieldString, Secret: true, Description: "Kubernetes bearer token (reference; never logged or emitted)"},
			{Key: cfgK8sTokenFile, Type: sdk.FieldString, Default: defaultK8sTokenFile, Description: "path to the ServiceAccount token file"},
			{Key: cfgK8sCAFile, Type: sdk.FieldString, Default: defaultK8sCAFile, Description: "path to the API server CA bundle"},
			{Key: cfgK8sNamespaces, Type: sdk.FieldString, Description: "comma-separated namespaces to scope pods to (empty ⇒ cluster-wide)"},
			{Key: cfgK8sInsecureSkipVerify, Type: sdk.FieldBool, Default: "false", Description: "skip API server TLS verification (lab use only)"},

			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-discoverer HTTP timeout"},
		},
	}
}

// loadConfig resolves the connector configuration from cfg, applying defaults.
// It never fails: an unconfigured/absent target is simply skipped at Gather; the
// host falls back to os.Hostname when unset.
func loadConfig(cfg sdk.Config) config {
	c := config{
		enableLinux:  cfg.GetBool(cfgEnableLinux, true),
		procRoot:     valueOr(cfg.Get(cfgProcRoot), defaultProcRoot),
		host:         cfg.Get(cfgHost),
		aiPatterns:   splitCSV(valueOr(cfg.Get(cfgAIPatterns), defaultAIPatterns)),
		enableDocker: cfg.GetBool(cfgEnableDocker, false),
		dockerSocket: valueOr(cfg.Get(cfgDockerSocket), defaultDockerSocket),

		enableK8s:             cfg.GetBool(cfgEnableK8s, true),
		k8sAPIServer:          strings.TrimSpace(cfg.Get(cfgK8sAPIServer)),
		k8sToken:              cfg.Get(cfgK8sToken),
		k8sTokenFile:          valueOr(cfg.Get(cfgK8sTokenFile), defaultK8sTokenFile),
		k8sCAFile:             valueOr(cfg.Get(cfgK8sCAFile), defaultK8sCAFile),
		k8sNamespaces:         splitCSV(cfg.Get(cfgK8sNamespaces)),
		k8sInsecureSkipVerify: cfg.GetBool(cfgK8sInsecureSkipVerify, false),

		timeout: cfg.GetDuration(cfgTimeout, defaultTimeout),
	}
	if c.host == "" {
		if h, err := os.Hostname(); err == nil {
			c.host = h
		}
		if c.host == "" {
			c.host = "localhost"
		}
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	return c
}

// valueOr returns v trimmed, or def when v is empty after trimming.
func valueOr(v, def string) string {
	if t := strings.TrimSpace(v); t != "" {
		return t
	}
	return def
}

// splitCSV splits a comma-separated value into trimmed, non-empty, lowercased
// tokens. Patterns and namespaces are matched case-insensitively, so lowercasing
// here lets the match sites compare against an already-lowered haystack.
func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if t := strings.ToLower(strings.TrimSpace(part)); t != "" {
			out = append(out, t)
		}
	}
	return out
}
