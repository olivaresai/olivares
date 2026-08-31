// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.gitlab-source"

const (
	// defaultWebhookAddr is LOOPBACK. It was the wildcard ":9801" until
	// which meant an operator who set nothing got a plaintext receiver on every
	// interface. GitLab cannot deliver to loopback directly, and that is the
	// point: the supported shape is a TLS gateway in front that forwards here.
	defaultWebhookAddr = "127.0.0.1:9801"

	cfgAllowPublicBind = "allow_public_bind"
)

// Source observes GitLab projects via webhooks and API polling, emitting
// EdgeObservation records for observed and permitted access.
type Source struct {
	group           string
	apiBase         string
	webhookAddr     string
	allowPublicBind bool
	webhookSecret   string
	token           string
	pollInterval    time.Duration
	aclInterval     time.Duration
	agentMarkers    map[string]struct{}
	botAccounts     map[string]struct{}
	client          *http.Client
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a GitLab source with an unconfigured default state.
func New() *Source { return &Source{client: &http.Client{Timeout: 30 * time.Second}} }

// Descriptor returns the connector's self-description.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "GitLab source",
		Description: "Observes GitLab projects via webhooks and API polling, emitting R/RW access edges.",
		ConfigFields: []sdk.ConfigField{
			{Key: "group", Type: sdk.FieldString, Required: true, Description: "top-level GitLab group to observe"},
			{Key: "webhook_address", Type: sdk.FieldString, Default: defaultWebhookAddr, Description: "Listen address for the webhook HTTP server. Loopback by default; the receiver serves PLAINTEXT HTTP, so a non-loopback bind is refused unless allow_public_bind=true. Front it with a TLS gateway that forwards to this address."},
			{Key: cfgAllowPublicBind, Type: sdk.FieldBool, Default: "false", Description: "DANGEROUS: allow binding the webhook receiver to a non-loopback address. The receiver has no TLS, so the delivery body and its secret token header would cross the network in the clear; keep loopback and terminate TLS in front of it."},
			{Key: "webhook_secret", Type: sdk.FieldString, Required: true, Secret: true, Description: "GitLab webhook secret token (secret-store ref)"},
			{Key: "token", Type: sdk.FieldString, Required: true, Secret: true, Description: "GitLab access token with api scope (secret-store ref)"},
			{Key: "api_base", Type: sdk.FieldString, Default: "https://gitlab.com", Description: "GitLab instance URL for self-managed"},
			{Key: "poll_interval", Type: sdk.FieldDuration, Default: "5m", Description: "API polling interval"},
			{Key: "acl_interval", Type: sdk.FieldDuration, Default: "15m", Description: "ACL sync interval"},
			{Key: "agent_markers", Type: sdk.FieldString, Default: "Claude,Copilot,Cursor,Cline,Codex,Devin,Aider,Windsurf", Description: "Co-Authored-By names indicating AI agents"},
			{Key: "bot_accounts", Type: sdk.FieldString, Description: "comma-separated bot usernames"},
		},
	}
}

// Open validates and applies resolved configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.group = cfg.Get("group")
	if s.group == "" {
		return fmt.Errorf("gitlab: group is required")
	}

	s.token = cfg.Get("token")
	if s.token == "" {
		return fmt.Errorf("gitlab: token is required")
	}

	s.webhookSecret = cfg.Get("webhook_secret")
	if s.webhookSecret == "" {
		return fmt.Errorf("gitlab: webhook_secret is required")
	}

	s.apiBase = cfg.Get("api_base")
	if s.apiBase == "" {
		s.apiBase = "https://gitlab.com"
	}
	s.apiBase = strings.TrimRight(s.apiBase, "/")

	s.webhookAddr = cfg.Get("webhook_address")
	if s.webhookAddr == "" {
		s.webhookAddr = defaultWebhookAddr
	}
	s.allowPublicBind = cfg.GetBool(cfgAllowPublicBind, false)
	// Deny-closed at CONFIG time, before Gather ever binds. The same decision is
	// enforced again at the socket itself in gather.go.
	if err := netbind.Check(s.webhookAddr, s.bindPolicy()); err != nil {
		return fmt.Errorf("gitlab: %w", err)
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
		Component:   "gitlab",
		Purpose:     "webhook receiver",
		AllowPublic: s.allowPublicBind,
		OptIn:       cfgAllowPublicBind,
	}
}

// Close releases resources; this connector holds none beyond the HTTP client.
func (s *Source) Close(context.Context) error { return nil }

func parseCSVSet(csv string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, v := range strings.Split(csv, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			m[strings.ToLower(v)] = struct{}{}
		}
	}
	return m
}
