// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrock

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.bedrock"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgAccessKeyID      = "access_key_id"
	cfgSecretAccessKey  = "secret_access_key"
	cfgSessionToken     = "session_token"
	cfgRegion           = "region"
	cfgAccountID        = "account_id"
	cfgUsageLogPath     = "usage_log_path"
	cfgUsageLogGroup    = "usage_log_group"
	cfgEnableCost       = "enable_cost"
	cfgEnableGuardrails = "enable_guardrails"
	cfgCostService      = "cost_service"
	cfgBedrockEndpoint  = "bedrock_endpoint"
	cfgCostEndpoint     = "cost_explorer_endpoint"
	cfgCWLogsEndpoint   = "cloudwatch_logs_endpoint"
	cfgUsageLookback    = "usage_lookback"
	cfgCostLookback     = "cost_lookback"
	cfgMaxEvents        = "max_events"
	cfgMaxGuardrails    = "max_guardrails"
	cfgTimeout          = "timeout"
)

// Environment-variable fallbacks for credentials, used when the corresponding
// config field is absent. They mirror the conventional AWS SDK variable names.
const (
	envAccessKeyID     = "AWS_ACCESS_KEY_ID"
	envSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	envSessionToken    = "AWS_SESSION_TOKEN"
)

// Defaults.
const (
	defaultRegion        = "us-east-1"
	defaultCostService   = "Amazon Bedrock"
	defaultUsageLookback = time.Hour
	defaultCostLookback  = 30 * 24 * time.Hour
	defaultMaxEvents     = 10000
	defaultMaxGuardrails = 100
	defaultTimeout       = 30 * time.Second
)

// SigV4 service names and the Cost Explorer signing region. Bedrock control plane and
// CloudWatch Logs are REGIONAL (signed under the operating region); Cost Explorer is a
// GLOBAL service whose single endpoint lives in us-east-1, so it is always signed there
// regardless of where Bedrock runs (verified jun-2026, primary AWS docs).
const (
	bedrockSigningService = "bedrock"
	cwLogsSigningService  = "logs"
	costSigningService    = "ce"
	costSigningRegion     = "us-east-1"
)

// config is the resolved connector configuration. The credential fields hold secret
// values in memory only; they are never logged or emitted.
type config struct {
	creds awsCreds

	region    string
	accountID string

	// usageLogPath, when set, is a local file or directory of S3-DELIVERED Bedrock
	// model-invocation-log files (*.json / *.json.gz). Reading it needs no AWS
	// credentials (mirrors the s3-cloudtrail file path). Empty ⇒ that source is off.
	usageLogPath string
	// usageLogGroup, when set, is the CloudWatch Logs group the model-invocation logs
	// are delivered to; the connector pulls it via FilterLogEvents (needs creds).
	// Empty ⇒ that source is off.
	usageLogGroup string

	enableCost       bool
	enableGuardrails bool

	// costService is the Cost Explorer SERVICE dimension value to filter Bedrock spend
	// on. AWS does not document the literal value; it is "Amazon Bedrock" in practice,
	// exposed as config so an operator can correct it (or resolve it via
	// GetDimensionValues) without a code change rather than have the connector silently
	// report zero cost on a value mismatch.
	costService string

	bedrockEndpoint string
	costEndpoint    string
	cwLogsEndpoint  string

	usageLookback time.Duration
	costLookback  time.Duration

	maxEvents     int
	maxGuardrails int
	timeout       time.Duration
}

// accountScope is the non-sensitive subject reference for the account+region scoped
// posture/health findings. Bedrock guardrails and usage are regional, so the region is
// part of the subject identity.
func (c config) accountScope() string {
	acct := c.accountID
	if acct == "" {
		acct = "aws"
	}
	return acct + "/" + c.region
}

// needsCreds reports whether any enabled source makes a live, signed AWS API call.
// Reading S3-delivered log FILES from usageLogPath is local I/O and needs no
// credentials, so a path-only configuration is valid without them.
func (c config) needsCreds() bool {
	return c.usageLogGroup != "" || c.enableCost || c.enableGuardrails
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "AWS Bedrock usage + cost + Guardrails",
		Description: "Read-only Amazon Bedrock observability beyond Claude: per-model token usage from model-invocation logging (S3-delivered files and/or CloudWatch Logs) emitted as CostSamples (token counts only, never model input/output), billed cost from Cost Explorer (Provenance=billed; 0 when no billed row — never fabricated), and Bedrock Guardrails configuration as read-only safety_posture findings (content/topic/word/PII/contextual-grounding + Automated Reasoning). Never reads payloads, secrets or key material; never calls the paid ApplyGuardrail runtime.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgAccessKeyID, Type: sdk.FieldString, Secret: true, Description: "AWS access key id (falls back to AWS_ACCESS_KEY_ID; not needed for usage_log_path-only)"},
			{Key: cfgSecretAccessKey, Type: sdk.FieldString, Secret: true, Description: "AWS secret access key (falls back to AWS_SECRET_ACCESS_KEY)"},
			{Key: cfgSessionToken, Type: sdk.FieldString, Secret: true, Description: "optional STS session token (falls back to AWS_SESSION_TOKEN)"},
			{Key: cfgRegion, Type: sdk.FieldString, Default: defaultRegion, Description: "AWS region for Bedrock control plane + CloudWatch Logs (Cost Explorer is global/us-east-1)"},
			{Key: cfgAccountID, Type: sdk.FieldString, Description: "optional AWS account id, used in the posture/health subject refs"},
			{Key: cfgUsageLogPath, Type: sdk.FieldString, Description: "local file or directory of S3-delivered Bedrock model-invocation-log files (*.json / *.json.gz); empty disables the S3 usage source"},
			{Key: cfgUsageLogGroup, Type: sdk.FieldString, Description: "CloudWatch Logs group the Bedrock model-invocation logs are delivered to (pulled via FilterLogEvents); empty disables the CloudWatch usage source"},
			{Key: cfgEnableCost, Type: sdk.FieldBool, Default: "false", Description: "read billed Bedrock cost from Cost Explorer GetCostAndUsage (opt-in; off by default)"},
			{Key: cfgEnableGuardrails, Type: sdk.FieldBool, Default: "false", Description: "read Bedrock Guardrails config + model-invocation-logging posture as safety_posture findings (opt-in; off by default)"},
			{Key: cfgCostService, Type: sdk.FieldString, Default: defaultCostService, Description: `Cost Explorer SERVICE dimension value for Bedrock (default "Amazon Bedrock"; override if your account reports a different service name)`},
			{Key: cfgBedrockEndpoint, Type: sdk.FieldString, Description: "Bedrock control-plane endpoint base URL (default https://bedrock.<region>.amazonaws.com)"},
			{Key: cfgCostEndpoint, Type: sdk.FieldString, Description: "Cost Explorer endpoint base URL (default https://ce.us-east-1.amazonaws.com)"},
			{Key: cfgCWLogsEndpoint, Type: sdk.FieldString, Description: "CloudWatch Logs endpoint base URL (default https://logs.<region>.amazonaws.com)"},
			{Key: cfgUsageLookback, Type: sdk.FieldDuration, Default: defaultUsageLookback.String(), Description: "CloudWatch Logs lookback window for model-invocation logs"},
			{Key: cfgCostLookback, Type: sdk.FieldDuration, Default: defaultCostLookback.String(), Description: "Cost Explorer lookback window (day-bucketed)"},
			{Key: cfgMaxEvents, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxEvents), Description: "max CloudWatch model-invocation log events per pass (bound)"},
			{Key: cfgMaxGuardrails, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxGuardrails), Description: "max Bedrock guardrails to read config for per pass (bound)"},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-request HTTP timeout"},
		},
	}
}

// loadConfig resolves the connector configuration from cfg, applying defaults and
// credential env fallbacks, deriving the regional endpoints when not given, and
// validating that credentials are present when a source that needs them is enabled. A
// config error surfaces in Open, before Gather (per the SDK contract). Secret values
// are read here and held in memory only.
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		region:           firstNonEmpty(strings.TrimSpace(cfg.Get(cfgRegion)), defaultRegion),
		accountID:        strings.TrimSpace(cfg.Get(cfgAccountID)),
		usageLogPath:     strings.TrimSpace(cfg.Get(cfgUsageLogPath)),
		usageLogGroup:    strings.TrimSpace(cfg.Get(cfgUsageLogGroup)),
		enableCost:       cfg.GetBool(cfgEnableCost, false),
		enableGuardrails: cfg.GetBool(cfgEnableGuardrails, false),
		costService:      firstNonEmpty(strings.TrimSpace(cfg.Get(cfgCostService)), defaultCostService),
		bedrockEndpoint:  strings.TrimSpace(cfg.Get(cfgBedrockEndpoint)),
		costEndpoint:     strings.TrimSpace(cfg.Get(cfgCostEndpoint)),
		cwLogsEndpoint:   strings.TrimSpace(cfg.Get(cfgCWLogsEndpoint)),
		usageLookback:    cfg.GetDuration(cfgUsageLookback, defaultUsageLookback),
		costLookback:     cfg.GetDuration(cfgCostLookback, defaultCostLookback),
		maxEvents:        cfg.GetInt(cfgMaxEvents, defaultMaxEvents),
		maxGuardrails:    cfg.GetInt(cfgMaxGuardrails, defaultMaxGuardrails),
		timeout:          cfg.GetDuration(cfgTimeout, defaultTimeout),
	}

	c.creds = awsCreds{
		akid:   firstNonEmpty(strings.TrimSpace(cfg.Get(cfgAccessKeyID)), strings.TrimSpace(os.Getenv(envAccessKeyID))),
		secret: firstNonEmpty(cfg.Get(cfgSecretAccessKey), os.Getenv(envSecretAccessKey)),
		token:  firstNonEmpty(cfg.Get(cfgSessionToken), os.Getenv(envSessionToken)),
	}

	if c.usageLookback <= 0 {
		c.usageLookback = defaultUsageLookback
	}
	if c.costLookback <= 0 {
		c.costLookback = defaultCostLookback
	}
	if c.maxEvents <= 0 {
		c.maxEvents = defaultMaxEvents
	}
	if c.maxGuardrails <= 0 {
		c.maxGuardrails = defaultMaxGuardrails
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	if c.bedrockEndpoint == "" {
		c.bedrockEndpoint = "https://bedrock." + c.region + ".amazonaws.com"
	}
	if c.costEndpoint == "" {
		c.costEndpoint = "https://ce." + costSigningRegion + ".amazonaws.com"
	}
	if c.cwLogsEndpoint == "" {
		c.cwLogsEndpoint = "https://logs." + c.region + ".amazonaws.com"
	}

	// Credentials are required only when a source that makes a signed AWS call is
	// enabled. A fully-disabled connector, or a usage_log_path-only one, is a valid
	// (no live API) configuration.
	if c.needsCreds() {
		if c.creds.akid == "" || c.creds.secret == "" {
			return config{}, fmt.Errorf("bedrock: missing credentials (set %q/%q or %s/%s) — required for CloudWatch usage, cost, or guardrails",
				cfgAccessKeyID, cfgSecretAccessKey, envAccessKeyID, envSecretAccessKey)
		}
	}
	return c, nil
}

// firstNonEmpty returns the first non-empty argument, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
