// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeappsgateway

import (
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.claude-apps-gateway"

const version = "0.1.0"

const (
	cfgConfigPath           = "config_path"
	cfgAuditLogPath         = "audit_log_path"
	cfgEndpoint             = "endpoint"
	cfgDeclaredSettingsPath = "declared_settings_path"
)

type config struct {
	configPath           string
	auditLogPath         string
	endpoint             string
	declaredSettingsPath string
}

func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude apps gateway posture and audit ingest",
		Description: "Reads gateway.yaml metadata, reports Claude apps gateway posture, and ingests minimal-data audit-event JSONL.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgConfigPath, Type: sdk.FieldString, Description: "optional path to the Claude apps gateway gateway.yaml"},
			{Key: cfgAuditLogPath, Type: sdk.FieldString, Description: "optional path to exported gateway audit events, one JSON object per line"},
			{Key: cfgEndpoint, Type: sdk.FieldString, Description: "optional base URL of a running gateway for unauthenticated discovery/protocol probes"},
			{Key: cfgDeclaredSettingsPath, Type: sdk.FieldString, Description: "optional path to the Olivares-declared managed-settings JSON used for policy drift comparison"},
		},
	}
}

func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		configPath:           strings.TrimSpace(cfg.Get(cfgConfigPath)),
		auditLogPath:         strings.TrimSpace(cfg.Get(cfgAuditLogPath)),
		endpoint:             strings.TrimRight(strings.TrimSpace(cfg.Get(cfgEndpoint)), "/"),
		declaredSettingsPath: strings.TrimSpace(cfg.Get(cfgDeclaredSettingsPath)),
	}
	if c.configPath == "" && c.auditLogPath == "" && c.endpoint == "" {
		return config{}, fmt.Errorf("at least one of config_path, audit_log_path, or endpoint is required")
	}
	return c, nil
}
