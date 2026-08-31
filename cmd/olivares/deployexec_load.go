// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"os"
	"strings"
	"time"

	executor "github.com/olivaresai/olivares/core/runtime/executor"
)

// This file loads the deploy executor from the operator-provisioned config
// (OLIVARES_DEPLOY_EXECUTOR_CONFIG) and assembles the real, governed actuation
// engine. It mirrors loadNotifyDestinations / loadApprovalBridgeConfig: an absent
// path leaves the module's deny-closed unwiredExecutor in place (apply/retire stay
// honest 503), while a supplied unreadable/invalid file fails startup. Secrets
// (service tokens, credential paths) live ONLY here, never in the module store.

// deployExecutorConfig is the operator's deploy-actuation provisioning. Each backend
// block is OPTIONAL; only the configured runtimes are wired (selection by runtime,
// never hardcoded). The credential, blast-radius, identity-binding and drift blocks
// are the cross-cutting governance knobs.
type deployExecutorConfig struct {
	Tofu        *tofuCfgJSON        `json:"tofu,omitempty"`
	Terraform   *tofuCfgJSON        `json:"terraform,omitempty"`
	GitOps      *gitopsCfgJSON      `json:"gitops,omitempty"`
	Kube        *kubeCfgJSON        `json:"k8s,omitempty"`
	Docker      *dockerCfgJSON      `json:"docker,omitempty"`
	Nomad       *nomadCfgJSON       `json:"nomad,omitempty"`
	Crossplane  *crossplaneCfgJSON  `json:"crossplane,omitempty"`
	Credential  credentialCfgJSON   `json:"credential,omitempty"`
	BlastRadius *blastRadiusCfgJSON `json:"blast_radius,omitempty"`
	Identity    struct {
		Tenants []identityTenantCfg `json:"tenants"`
	} `json:"identity_binding,omitempty"`
	Drift struct {
		IntervalSeconds int              `json:"interval_seconds"`
		Tenants         []driftTenantCfg `json:"tenants"`
	} `json:"drift,omitempty"`
}

type tofuCfgJSON struct {
	Binary             string   `json:"binary"`
	WorkdirRoot        string   `json:"workdir_root"`
	CredentialEnv      []string `json:"credential_env"`
	PassthroughEnv     []string `json:"passthrough_env"`
	AllowAmbientCreds  bool     `json:"allow_ambient_creds"`
	LockTimeoutSeconds int      `json:"lock_timeout_seconds"`
	TimeoutSeconds     int      `json:"timeout_seconds"`
}

type gitopsCfgJSON struct {
	Binary           string `json:"binary"`
	WorkdirRoot      string `json:"workdir_root"`
	Branch           string `json:"branch"`
	Remote           string `json:"remote"`
	PathPrefix       string `json:"path_prefix"`
	Namespace        string `json:"namespace"`
	AuthorName       string `json:"author_name"`
	AuthorEmail      string `json:"author_email"`
	GitUsername      string `json:"git_username"`
	StatusController string `json:"status_controller"`
	StatusBaseURL    string `json:"status_base_url"`
	StatusNamespace  string `json:"status_namespace"`
	StatusAppName    string `json:"status_app_name"`
	StatusCAFile     string `json:"status_ca_file"`
	StatusInsecure   bool   `json:"status_insecure"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
}

type kubeCfgJSON struct {
	APIBaseURL         string `json:"api_base_url"`
	CAFile             string `json:"ca_file"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	DefaultNamespace   string `json:"default_namespace"`
	TimeoutSeconds     int    `json:"timeout_seconds"`
}

type dockerCfgJSON struct {
	SocketPath     string `json:"socket_path"`
	RemoteBaseURL  string `json:"remote_base_url"`
	RemoteCAFile   string `json:"remote_ca_file"`
	RemoteInsecure bool   `json:"remote_insecure"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type nomadCfgJSON struct {
	BaseURL            string   `json:"base_url"`
	CAFile             string   `json:"ca_file"`
	InsecureSkipVerify bool     `json:"insecure_skip_verify"`
	Namespace          string   `json:"namespace"`
	Datacenters        []string `json:"datacenters"`
	TimeoutSeconds     int      `json:"timeout_seconds"`
}

type crossplaneCfgJSON struct {
	APIServer      string `json:"api_server"`
	CAFile         string `json:"ca_file"`
	Insecure       bool   `json:"insecure"`
	APIGroup       string `json:"api_group"`
	APIVersion     string `json:"api_version"`
	Plural         string `json:"plural"`
	Kind           string `json:"kind"`
	Namespaced     bool   `json:"namespaced"`
	Namespace      string `json:"namespace"`
	FieldManager   string `json:"field_manager"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type credentialCfgJSON struct {
	// Kind selects the executor's short-lived credential source:
	//   - "wif": the in-process WIF broker — mints a short-lived sk-ant-oat under the
	//     named tenant's federation rule (the executor MintRequest carries no tenant, so the
	//     operator names it here in Tenant, optionally pinning FederationRuleID). Deny-closed:
	//     no broker, no/invalid tenant, or a mint failure fails every actuation closed.
	//   - "file": a FileTokenSource over a rotated, per-environment token file written by an
	//     external attester (Vault Agent / SPIFFE helper) — the compatibility path.
	//   - "" / anything else: the deny-closed default (every actuation fails closed).
	Kind         string `json:"kind"`
	PathTemplate string `json:"path_template"`
	TTLSeconds   int    `json:"ttl_seconds"`
	Scheme       string `json:"scheme"`
	// Tenant / FederationRuleID configure Kind "wif": the tenant whose declared claude-wif
	// federation rule the executor mints under, and (optional) which rule when the tenant
	// declares more than one. Ignored for other kinds.
	Tenant           string `json:"tenant"`
	FederationRuleID string `json:"federation_rule_id"`
}

type blastRadiusCfgJSON struct {
	MaxApplyDestructive int   `json:"max_apply_destructive"`
	AllowDestroy        *bool `json:"allow_destroy"`
	MaxDestroyItems     int   `json:"max_destroy_items"`
}

// loadDeployExecutorConfig reads OLIVARES_DEPLOY_EXECUTOR_CONFIG. A missing path is an
// empty config (executor not wired; honest 503). A supplied path must be readable and
// contain valid JSON or startup fails closed.
func loadDeployExecutorConfig(_ *slog.Logger) (deployExecutorConfig, error) {
	path := os.Getenv("OLIVARES_DEPLOY_EXECUTOR_CONFIG")
	if path == "" {
		return deployExecutorConfig{}, nil
	}
	var cfg deployExecutorConfig
	if err := loadOperatorJSONConfig("OLIVARES_DEPLOY_EXECUTOR_CONFIG", path, &cfg); err != nil {
		return deployExecutorConfig{}, err
	}
	return cfg, nil
}

// newDeployExecutor builds the real deploy.Executor adapter from config, or nil when
// no backend is configured (the module then keeps its deny-closed unwiredExecutor).
func newDeployExecutor(cfg deployExecutorConfig, broker *wifCredentialBroker, log *slog.Logger) *deployExecutor {
	opts := []executor.Option{executor.WithLogger(log), executor.WithCredentialSource(cfg.Credential.sourceWith(broker, log))}
	if p, ok := cfg.BlastRadius.policy(); ok {
		opts = append(opts, executor.WithBlastRadiusPolicy(p))
	}

	wired := []string{}
	if cfg.Tofu != nil {
		opts = append(opts, executor.WithBackend(executor.NewTofuBackend(cfg.Tofu.to("tofu")), "tofu"))
		wired = append(wired, "tofu")
	}
	if cfg.Terraform != nil {
		opts = append(opts, executor.WithBackend(executor.NewTofuBackend(cfg.Terraform.to("terraform")), "terraform"))
		wired = append(wired, "terraform")
	}
	if cfg.GitOps != nil {
		opts = append(opts, executor.WithBackend(executor.NewGitOpsBackend(cfg.GitOps.to(log)), "gitops"))
		wired = append(wired, "gitops")
	}
	if cfg.Kube != nil {
		opts = append(opts, executor.WithBackend(executor.NewKubeBackend(cfg.Kube.to(log)), "k8s", "kubernetes"))
		wired = append(wired, "k8s")
	}
	if cfg.Docker != nil {
		opts = append(opts, executor.WithBackend(executor.NewDockerBackend(cfg.Docker.to(log)), "docker"))
		wired = append(wired, "docker")
	}
	if cfg.Nomad != nil {
		opts = append(opts, executor.WithBackend(executor.NewNomadBackend(cfg.Nomad.to(log)), "nomad"))
		wired = append(wired, "nomad")
	}
	if cfg.Crossplane != nil {
		opts = append(opts, executor.WithBackend(executor.NewCrossplaneBackend(cfg.Crossplane.to(log)), "crossplane"))
		wired = append(wired, "crossplane")
	}
	if len(wired) == 0 {
		return nil
	}
	log.Info("deploy-executor: real executor wired (module VII now ACTS)", "runtimes", strings.Join(wired, ","),
		"credential_source", cfg.Credential.label())
	return &deployExecutor{e: executor.New(opts...)}
}

// --- mappers ---------------------------------------------------------------------

func secs(n int) time.Duration { return time.Duration(n) * time.Second }

// sourceWith resolves the executor credential source, wiring the in-process WIF broker
// for Kind "wif" and otherwise deferring to source() (file / deny). Deny-closed: an
// opted-in "wif" with no broker, or an invalid/missing tenant, fails every actuation closed
// — never a static fallback.
func (c credentialCfgJSON) sourceWith(broker *wifCredentialBroker, log *slog.Logger) executor.CredentialSource {
	if strings.EqualFold(c.Kind, "wif") {
		if broker == nil {
			if log != nil {
				log.Warn("deploy-executor: credential kind=wif but the WIF broker is unavailable; actuation deny-closed")
			}
			return executor.DenyCredentialSource{}
		}
		tid, present, err := parseBusinessTenant("deploy-executor credential: tenant", c.Tenant)
		if err != nil || !present {
			if log != nil {
				log.Warn("deploy-executor: credential kind=wif requires a valid business tenant id; actuation deny-closed", "tenant", c.Tenant)
			}
			return executor.DenyCredentialSource{}
		}
		return broker.executorSource(tid, strings.TrimSpace(c.FederationRuleID))
	}
	return c.source()
}

func (c credentialCfgJSON) source() executor.CredentialSource {
	if strings.EqualFold(c.Kind, "file") {
		return executor.NewFileTokenSource(executor.FileTokenConfig{PathTemplate: c.PathTemplate, TTL: secs(c.TTLSeconds), Scheme: c.Scheme})
	}
	return executor.DenyCredentialSource{}
}

func (c credentialCfgJSON) label() string {
	switch {
	case strings.EqualFold(c.Kind, "wif"):
		return "wif (in-process ephemeral mint; deny-closed if unavailable)"
	case strings.EqualFold(c.Kind, "file"):
		return "file-token (deny-closed if absent)"
	default:
		return "deny-closed (none)"
	}
}

func (c *blastRadiusCfgJSON) policy() (executor.BlastRadiusPolicy, bool) {
	if c == nil {
		return executor.BlastRadiusPolicy{}, false
	}
	allow := true
	if c.AllowDestroy != nil {
		allow = *c.AllowDestroy
	}
	return executor.BlastRadiusPolicy{MaxApplyDestructive: c.MaxApplyDestructive, AllowDestroy: allow, MaxDestroyItems: c.MaxDestroyItems}, true
}

func (c tofuCfgJSON) to(binary string) executor.TofuConfig {
	if c.Binary != "" {
		binary = c.Binary
	}
	return executor.TofuConfig{
		Binary: binary, WorkdirRoot: c.WorkdirRoot, CredentialEnv: c.CredentialEnv,
		PassthroughEnv: c.PassthroughEnv, AllowAmbientCreds: c.AllowAmbientCreds,
		LockTimeout: secs(c.LockTimeoutSeconds), Timeout: secs(c.TimeoutSeconds),
	}
}

func (c gitopsCfgJSON) to(log *slog.Logger) executor.GitOpsConfig {
	return executor.GitOpsConfig{
		Binary: c.Binary, WorkdirRoot: c.WorkdirRoot, Branch: c.Branch, Remote: c.Remote,
		PathPrefix: c.PathPrefix, Namespace: c.Namespace, AuthorName: c.AuthorName,
		AuthorEmail: c.AuthorEmail, GitUsername: c.GitUsername, StatusController: c.StatusController,
		StatusBaseURL: c.StatusBaseURL, StatusNamespace: c.StatusNamespace, StatusAppName: c.StatusAppName,
		StatusCABundle: readPEMFile(c.StatusCAFile, log), StatusInsecure: c.StatusInsecure, Timeout: secs(c.TimeoutSeconds),
	}
}

func (c kubeCfgJSON) to(log *slog.Logger) executor.KubeConfig {
	return executor.KubeConfig{
		APIBaseURL: c.APIBaseURL, CABundlePEM: readPEMFile(c.CAFile, log), InsecureSkipVerify: c.InsecureSkipVerify,
		DefaultNamespace: c.DefaultNamespace, Timeout: secs(c.TimeoutSeconds),
	}
}

func (c dockerCfgJSON) to(log *slog.Logger) executor.DockerConfig {
	return executor.DockerConfig{
		SocketPath: c.SocketPath, RemoteBaseURL: c.RemoteBaseURL, RemoteCAPEM: readPEMFile(c.RemoteCAFile, log),
		RemoteInsecure: c.RemoteInsecure, Timeout: secs(c.TimeoutSeconds),
	}
}

func (c nomadCfgJSON) to(log *slog.Logger) executor.NomadConfig {
	return executor.NomadConfig{
		BaseURL: c.BaseURL, CABundle: readPEMFile(c.CAFile, log), InsecureSkipVerify: c.InsecureSkipVerify,
		Namespace: c.Namespace, Datacenters: c.Datacenters, Timeout: secs(c.TimeoutSeconds),
	}
}

func (c crossplaneCfgJSON) to(log *slog.Logger) executor.CrossplaneConfig {
	return executor.CrossplaneConfig{
		APIServer: c.APIServer, CABundle: readPEMFile(c.CAFile, log), Insecure: c.Insecure,
		APIGroup: c.APIGroup, APIVersion: c.APIVersion, Plural: c.Plural, Kind: c.Kind,
		Namespaced: c.Namespaced, Namespace: c.Namespace, FieldManager: c.FieldManager, Timeout: secs(c.TimeoutSeconds),
	}
}

// readPEMFile reads a CA bundle from a path (a public cert, not a secret). A missing
// path returns nil (the backend then pins nothing / uses the system pool or the
// insecure opt-in). An unreadable file warns and returns nil — never a boot failure.
func readPEMFile(path string, log *slog.Logger) []byte {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		log.Warn("deploy-executor: cannot read CA file; backend will not pin this CA", "path", path)
		return nil
	}
	return b
}
