// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.aws"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgAccessKeyID        = "access_key_id"
	cfgSecretAccessKey    = "secret_access_key"
	cfgSessionToken       = "session_token"
	cfgRegion             = "region"
	cfgAccountID          = "account_id"
	cfgEnableIAM          = "enable_iam"
	cfgEnableCloudTrail   = "enable_cloudtrail"
	cfgEnableBedrock      = "enable_bedrock"
	cfgIAMEndpoint        = "iam_endpoint"
	cfgCloudTrailEndpoint = "cloudtrail_endpoint"
	cfgBedrockEndpoint    = "bedrock_endpoint"
	cfgPolicyScope        = "policy_scope"
	cfgLookback           = "lookback"
	cfgMaxEvents          = "max_events"
	cfgMaxGuardrails      = "max_guardrails"
	cfgTimeout            = "timeout"
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
	defaultIAMEndpoint   = "https://iam.amazonaws.com"
	defaultPolicyScope   = "Local"
	defaultLookback      = time.Hour
	defaultMaxEvents     = 1000
	defaultMaxGuardrails = 100
	defaultTimeout       = 30 * time.Second
)

// bedrockSigningService is the SigV4 service for the Bedrock control plane (the
// ListGuardrails/GetGuardrail/GetModelInvocationLoggingConfiguration reads). Bedrock
// is regional, so it is signed under the configured operating region (unlike IAM).
const bedrockSigningService = "bedrock"

// iamSigningRegion is the region IAM is signed under: IAM is a global service, so
// its requests are always signed for us-east-1 regardless of the operating region.
const iamSigningRegion = "us-east-1"

// iamSigningService and cloudTrailSigningService name the SigV4 services.
const (
	iamSigningService        = "iam"
	cloudTrailSigningService = "cloudtrail"
)

// config is the resolved connector configuration. The credential fields hold
// secret values in memory only; they are never logged or emitted.
type config struct {
	creds awsCreds

	region    string
	accountID string

	enableIAM        bool
	enableCloudTrail bool
	enableBedrock    bool

	iamEndpoint        string
	cloudTrailEndpoint string
	bedrockEndpoint    string

	policyScope   string
	lookback      time.Duration
	maxEvents     int
	maxGuardrails int
	timeout       time.Duration
}

// originAccountRef returns the natural reference for the AWS account that owns the
// discovered IAM resources: the configured account id, or the literal "aws" when
// no account id is supplied (the task contract).
func (c config) originAccountRef() string {
	if c.accountID != "" {
		return c.accountID
	}
	return "aws"
}

// bedrockAccountScope is the non-sensitive subject reference for the account+region
// scoped Bedrock posture findings (guardrail-absence and decision-logging). Bedrock
// guardrails are regional, so the region is part of the subject identity.
func (c config) bedrockAccountScope() string {
	return c.originAccountRef() + "/" + c.region
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "AWS IAM + CloudTrail + Bedrock Guardrails",
		Description: "Read-only AWS IAM inventory (metadata only), CloudTrail management-event audit feed, and (opt-in) Bedrock Guardrails safety posture: guardrail configuration + model-invocation-logging (decision-auditability) reads. Emits topology and control-plane edges and safety_posture findings; never reads payloads, secrets or key material.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgAccessKeyID, Type: sdk.FieldString, Secret: true, Description: "AWS access key id (falls back to AWS_ACCESS_KEY_ID)"},
			{Key: cfgSecretAccessKey, Type: sdk.FieldString, Secret: true, Description: "AWS secret access key (falls back to AWS_SECRET_ACCESS_KEY)"},
			{Key: cfgSessionToken, Type: sdk.FieldString, Secret: true, Description: "optional STS session token (falls back to AWS_SESSION_TOKEN)"},
			{Key: cfgRegion, Type: sdk.FieldString, Default: defaultRegion, Description: "AWS region for CloudTrail (IAM is global)"},
			{Key: cfgAccountID, Type: sdk.FieldString, Description: "optional AWS account id, used as the topology origin ref"},
			{Key: cfgEnableIAM, Type: sdk.FieldBool, Default: "true", Description: "discover IAM roles/users/policies/attachments"},
			{Key: cfgEnableCloudTrail, Type: sdk.FieldBool, Default: "true", Description: "read CloudTrail management events"},
			{Key: cfgEnableBedrock, Type: sdk.FieldBool, Default: "false", Description: "read Bedrock Guardrails config + model-invocation-logging posture (opt-in; off by default — enable on AWS accounts that use Bedrock)"},
			{Key: cfgIAMEndpoint, Type: sdk.FieldString, Default: defaultIAMEndpoint, Description: "IAM endpoint base URL (override for testing)"},
			{Key: cfgCloudTrailEndpoint, Type: sdk.FieldString, Description: "CloudTrail endpoint base URL (default https://cloudtrail.<region>.amazonaws.com)"},
			{Key: cfgBedrockEndpoint, Type: sdk.FieldString, Description: "Bedrock control-plane endpoint base URL (default https://bedrock.<region>.amazonaws.com)"},
			{Key: cfgPolicyScope, Type: sdk.FieldString, Default: defaultPolicyScope, Description: `IAM ListPolicies scope: "Local" or "All" (All includes AWS-managed)`},
			{Key: cfgLookback, Type: sdk.FieldDuration, Default: defaultLookback.String(), Description: "CloudTrail lookback window"},
			{Key: cfgMaxEvents, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxEvents), Description: "max CloudTrail events per pass"},
			{Key: cfgMaxGuardrails, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxGuardrails), Description: "max Bedrock guardrails to read config for per pass (bound)"},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-request HTTP timeout"},
		},
	}
}

// loadConfig resolves the connector configuration from cfg, applying defaults and
// credential env fallbacks, deriving the CloudTrail endpoint from the region when
// not given, and validating that credentials are present when a service that
// needs them is enabled. It returns a config error so Open can surface it before
// Gather (per the SDK contract). Secret values are read here and held in memory.
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		region:             firstNonEmpty(strings.TrimSpace(cfg.Get(cfgRegion)), defaultRegion),
		accountID:          strings.TrimSpace(cfg.Get(cfgAccountID)),
		enableIAM:          cfg.GetBool(cfgEnableIAM, true),
		enableCloudTrail:   cfg.GetBool(cfgEnableCloudTrail, true),
		enableBedrock:      cfg.GetBool(cfgEnableBedrock, false),
		iamEndpoint:        firstNonEmpty(strings.TrimSpace(cfg.Get(cfgIAMEndpoint)), defaultIAMEndpoint),
		cloudTrailEndpoint: strings.TrimSpace(cfg.Get(cfgCloudTrailEndpoint)),
		bedrockEndpoint:    strings.TrimSpace(cfg.Get(cfgBedrockEndpoint)),
		policyScope:        normalizePolicyScope(cfg.Get(cfgPolicyScope)),
		lookback:           cfg.GetDuration(cfgLookback, defaultLookback),
		maxEvents:          cfg.GetInt(cfgMaxEvents, defaultMaxEvents),
		maxGuardrails:      cfg.GetInt(cfgMaxGuardrails, defaultMaxGuardrails),
		timeout:            cfg.GetDuration(cfgTimeout, defaultTimeout),
	}

	c.creds = awsCreds{
		akid:   firstNonEmpty(strings.TrimSpace(cfg.Get(cfgAccessKeyID)), strings.TrimSpace(os.Getenv(envAccessKeyID))),
		secret: firstNonEmpty(cfg.Get(cfgSecretAccessKey), os.Getenv(envSecretAccessKey)),
		token:  firstNonEmpty(cfg.Get(cfgSessionToken), os.Getenv(envSessionToken)),
	}

	if c.lookback <= 0 {
		c.lookback = defaultLookback
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
	if c.cloudTrailEndpoint == "" {
		c.cloudTrailEndpoint = "https://cloudtrail." + c.region + ".amazonaws.com"
	}
	if c.bedrockEndpoint == "" {
		c.bedrockEndpoint = "https://bedrock." + c.region + ".amazonaws.com"
	}

	// Credentials are required only when a service that uses them is enabled. A
	// fully-disabled connector is a valid (no-op) configuration.
	if c.enableIAM || c.enableCloudTrail || c.enableBedrock {
		if c.creds.akid == "" || c.creds.secret == "" {
			return config{}, fmt.Errorf("aws: missing credentials (set %q/%q or %s/%s)",
				cfgAccessKeyID, cfgSecretAccessKey, envAccessKeyID, envSecretAccessKey)
		}
	}
	return c, nil
}

// normalizePolicyScope coerces the policy_scope setting to one of the two IAM
// values. Anything other than a case-insensitive "All" falls back to "Local"
// (the safe, account-local default), so a typo never silently widens the scope.
func normalizePolicyScope(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "All") {
		return "All"
	}
	return defaultPolicyScope
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
