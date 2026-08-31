// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.github-source"

const (
	// defaultWebhookAddr is LOOPBACK. It was the wildcard ":9800" until
	// which meant an operator who set nothing got a plaintext receiver on every
	// interface — a careless install turned into a public surface. GitHub cannot
	// deliver to loopback directly, and that is the point: the supported shape is
	// a TLS gateway in front that forwards here, exactly as the sibling receivers
	// in this tree already require.
	defaultWebhookAddr = "127.0.0.1:9800"

	cfgAllowPublicBind = "allow_public_bind"
)

// Source is the GitHub source connector. It observes repositories via
// webhooks and API polling, emitting EdgeObservations for the access map.
// The zero value is not usable; call New.
type Source struct {
	org             string
	apiBase         string
	webhookAddr     string
	allowPublicBind bool
	webhookSecret   string
	token           string
	appID           string
	installID       string
	privateKey      string
	pollInterval    time.Duration
	aclInterval     time.Duration
	agentMarkers    map[string]struct{}
	botAccounts     map[string]struct{}
	client          *http.Client
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a GitHub source with a default HTTP client.
func New() *Source {
	return &Source{client: &http.Client{Timeout: 30 * time.Second}}
}

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "GitHub Source",
		Description: "Observes GitHub repositories via webhooks and API polling, emitting R/RW access edges for the access map.",
		ConfigFields: []sdk.ConfigField{
			{Key: "org", Type: sdk.FieldString, Required: true, Description: "GitHub organization to observe"},
			{Key: "webhook_address", Type: sdk.FieldString, Default: defaultWebhookAddr, Description: "Listen address for the webhook HTTP server. Loopback by default; the receiver serves PLAINTEXT HTTP, so a non-loopback bind is refused unless allow_public_bind=true. Front it with a TLS gateway that forwards to this address."},
			{Key: cfgAllowPublicBind, Type: sdk.FieldBool, Default: "false", Description: "DANGEROUS: allow binding the webhook receiver to a non-loopback address. The receiver has no TLS, so the delivery body and its HMAC signature header would cross the network in the clear; keep loopback and terminate TLS in front of it."},
			{Key: "webhook_secret", Type: sdk.FieldString, Required: true, Secret: true, Description: "HMAC-SHA256 secret for webhook verification (secret-store ref)"},
			{Key: "app_id", Type: sdk.FieldString, Description: "GitHub App ID"},
			{Key: "installation_id", Type: sdk.FieldString, Description: "GitHub App installation ID"},
			{Key: "private_key", Type: sdk.FieldString, Secret: true, Description: "GitHub App private key PEM (secret-store ref)"},
			{Key: "pat", Type: sdk.FieldString, Secret: true, Description: "personal access token fallback (secret-store ref)"},
			{Key: "api_base", Type: sdk.FieldString, Default: "https://api.github.com", Description: "API base URL for GHE Server"},
			{Key: "poll_interval", Type: sdk.FieldDuration, Default: "5m", Description: "API polling interval"},
			{Key: "acl_interval", Type: sdk.FieldDuration, Default: "15m", Description: "ACL sync interval"},
			{Key: "agent_markers", Type: sdk.FieldString, Default: "Claude,Copilot,Cursor,Cline,Codex,Devin,Aider,Windsurf", Description: "Co-Authored-By names indicating AI agents"},
			{Key: "bot_accounts", Type: sdk.FieldString, Description: "comma-separated bot usernames"},
		},
	}
}

// Open validates and applies configuration. At least one auth method (GitHub
// App credentials or PAT) must be provided.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.org = strings.TrimSpace(cfg.Get("org"))
	if s.org == "" {
		return errors.New("github: org is required")
	}

	s.webhookAddr = cfg.Get("webhook_address")
	if s.webhookAddr == "" {
		s.webhookAddr = defaultWebhookAddr
	}
	s.allowPublicBind = cfg.GetBool(cfgAllowPublicBind, false)
	// Deny-closed at CONFIG time, before Gather ever binds: an operator who
	// configured an exposure must be told at startup, not discover it from a
	// packet capture. The same decision is enforced again at the socket itself
	// in gather.go — this one exists so the refusal arrives early and by name.
	if err := netbind.Check(s.webhookAddr, s.bindPolicy()); err != nil {
		return fmt.Errorf("github: %w", err)
	}
	s.webhookSecret = cfg.Get("webhook_secret")
	if s.webhookSecret == "" {
		return errors.New("github: webhook_secret is required")
	}

	s.appID = strings.TrimSpace(cfg.Get("app_id"))
	s.installID = strings.TrimSpace(cfg.Get("installation_id"))
	s.privateKey = cfg.Get("private_key")
	s.token = cfg.Get("pat")

	hasApp := s.appID != "" && s.installID != "" && s.privateKey != ""
	hasPAT := s.token != ""
	if !hasApp && !hasPAT {
		return errors.New("github: at least one auth method required (app_id+installation_id+private_key or pat)")
	}
	// Prefer App token; PAT is the fallback used directly.
	if hasApp {
		s.token = s.privateKey // placeholder — a real implementation would mint an installation token
	}

	s.apiBase = strings.TrimRight(cfg.Get("api_base"), "/")
	if s.apiBase == "" {
		s.apiBase = "https://api.github.com"
	}

	s.pollInterval = cfg.GetDuration("poll_interval", 5*time.Minute)
	s.aclInterval = cfg.GetDuration("acl_interval", 15*time.Minute)

	s.agentMarkers = parseCSVSet(cfg.Get("agent_markers"))
	if len(s.agentMarkers) == 0 {
		s.agentMarkers = parseCSVSet("Claude,Copilot,Cursor,Cline,Codex,Devin,Aider,Windsurf")
	}
	s.botAccounts = parseCSVSet(cfg.Get("bot_accounts"))

	return nil
}

// bindPolicy describes the webhook receiver to the single admission point. The
// receiver is plaintext: gather.go hands its mux to http.Server and never wraps
// it in TLS, so every bind it makes is governed by that fact.
func (s *Source) bindPolicy() netbind.Policy {
	return netbind.Policy{
		Component:   "github",
		Purpose:     "webhook receiver",
		AllowPublic: s.allowPublicBind,
		OptIn:       cfgAllowPublicBind,
	}
}

// Close releases resources. The webhook server is shut down in Gather.
func (s *Source) Close(context.Context) error { return nil }

// parseCSVSet splits a comma-separated string into a case-insensitive set,
// trimming whitespace and ignoring empty entries.
func parseCSVSet(csv string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, v := range strings.Split(csv, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		m[strings.ToLower(v)] = struct{}{}
	}
	return m
}
