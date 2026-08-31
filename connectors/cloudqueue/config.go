// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudqueue

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/sdk"
)

// SourceName / OutputName are the two globally unique dotted connector ids. The
// Source observes managed-bus topology; the Output publishes CloudEvents to a bus.
const (
	SourceName = "olivares.cloudqueue"
	OutputName = "olivares.cloudqueue-egress"
)

// version is the connector's own semantic version.
const version = "0.1.0"

// Provider values. The connector is pluggable across the two managed-bus clouds it
// covers; Azure buses are reached via the kafka/amqp connectors (see doc.go).
const (
	providerAWS = "aws"
	providerGCP = "gcp"
)

// Configuration keys (declared in the Descriptors, read in Open).
const (
	cfgProvider = "provider"

	// AWS.
	cfgRegion          = "region"
	cfgAccountID       = "account_id"
	cfgAccessKeyID     = "access_key_id"
	cfgSecretAccessKey = "secret_access_key"
	cfgSessionToken    = "session_token"
	cfgEnableSQS       = "enable_sqs"
	cfgEnableSNS       = "enable_sns"
	cfgEnableEvBridge  = "enable_eventbridge"
	cfgSQSEndpoint     = "sqs_endpoint"
	cfgSNSEndpoint     = "sns_endpoint"
	cfgEvBridgeEndpt   = "eventbridge_endpoint"

	// GCP.
	cfgProject        = "project"
	cfgAccessToken    = "access_token"
	cfgPubSubEndpoint = "pubsub_endpoint"

	// Egress (Output) + shared.
	cfgEgressTarget = "egress_target"
	cfgEgressSource = "egress_source"
	cfgOTel         = "otel_messaging"
	cfgTimeout      = "timeout"
)

// Environment-variable fallbacks for AWS credentials (conventional names).
const (
	envAccessKeyID     = "AWS_ACCESS_KEY_ID"
	envSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	envSessionToken    = "AWS_SESSION_TOKEN"
)

// Defaults.
const (
	defaultRegion       = "us-east-1"
	defaultEgressSource = "/olivares/olivares"
	defaultTimeout      = 30 * time.Second
)

// Default public endpoints (overridable per service so tests point at httptest).
const (
	defaultSNSEndpointTmpl   = "https://sns.%s.amazonaws.com"
	defaultSQSEndpointTmpl   = "https://sqs.%s.amazonaws.com"
	defaultEvBridgeEndptTmpl = "https://events.%s.amazonaws.com"
	defaultPubSubEndpoint    = "https://pubsub.googleapis.com"
)

// SigV4 service names.
const (
	sqsService         = "sqs"
	snsService         = "sns"
	eventBridgeService = "events"
)

// config is the resolved connector configuration shared by Source and Output.
// Credential fields hold secret values in memory only; never logged or emitted.
type config struct {
	provider string

	// AWS.
	creds     awssig.Creds
	region    string
	accountID string

	enableSQS bool
	enableSNS bool
	enableEvB bool

	sqsEndpoint string
	snsEndpoint string
	evbEndpoint string

	// GCP.
	project        string
	accessToken    string
	pubsubEndpoint string

	// Egress (Output).
	egressTarget string
	egressSource string

	otel    bool
	timeout time.Duration
}

// originRef is the natural origin reference for the cloud account/project that owns
// the discovered buses: the configured account id (AWS) or project (GCP), falling
// back to a stable literal so an edge always has a non-empty origin.
func (c config) originRef() string {
	switch c.provider {
	case providerGCP:
		if c.project != "" {
			return c.project
		}
		return "gcp"
	default:
		if c.accountID != "" {
			return c.accountID
		}
		return "aws"
	}
}

// sourceDescriptor is the Source connector's stable self-description.
func sourceDescriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:         SourceName,
		Version:      version,
		APIVersion:   sdk.APIVersion,
		Type:         sdk.TypeSource,
		Title:        "Managed cloud message buses (topology)",
		Description:  "Read-only topology observer for AWS SQS/SNS/EventBridge and GCP Pub/Sub; emits which buses exist and how they fan out (no message bodies).",
		ConfigFields: configFields(),
	}
}

// outputDescriptor is the Output (egress) connector's stable self-description.
func outputDescriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        OutputName,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Managed cloud message bus egress (CloudEvents)",
		Description: "Publishes Olivares notifications as CloudEvents to an SNS topic (aws) or a Pub/Sub topic (gcp).",
		ConfigFields: append(configFields(),
			sdk.ConfigField{Key: cfgEgressTarget, Type: sdk.FieldString, Required: true, Description: "egress destination: SNS topic ARN (aws) or Pub/Sub topic id (gcp)"},
			sdk.ConfigField{Key: cfgEgressSource, Type: sdk.FieldString, Default: defaultEgressSource, Description: "CloudEvents source URI for published events"},
		),
	}
}

// configFields are the settings both connectors share.
func configFields() []sdk.ConfigField {
	return []sdk.ConfigField{
		{Key: cfgProvider, Type: sdk.FieldString, Required: true, Description: `managed-bus provider: "aws" or "gcp"`},

		{Key: cfgRegion, Type: sdk.FieldString, Default: defaultRegion, Description: "AWS region (aws)"},
		{Key: cfgAccountID, Type: sdk.FieldString, Description: "optional AWS account id, used as the topology origin ref (aws)"},
		{Key: cfgAccessKeyID, Type: sdk.FieldString, Secret: true, Description: "AWS access key id (aws; falls back to AWS_ACCESS_KEY_ID)"},
		{Key: cfgSecretAccessKey, Type: sdk.FieldString, Secret: true, Description: "AWS secret access key (aws; falls back to AWS_SECRET_ACCESS_KEY)"},
		{Key: cfgSessionToken, Type: sdk.FieldString, Secret: true, Description: "optional STS session token (aws; falls back to AWS_SESSION_TOKEN)"},
		{Key: cfgEnableSQS, Type: sdk.FieldBool, Default: "true", Description: "discover SQS queues (aws)"},
		{Key: cfgEnableSNS, Type: sdk.FieldBool, Default: "true", Description: "discover SNS topics + subscription fan-out (aws)"},
		{Key: cfgEnableEvBridge, Type: sdk.FieldBool, Default: "true", Description: "discover EventBridge buses (aws)"},
		{Key: cfgSQSEndpoint, Type: sdk.FieldString, Description: "SQS endpoint base URL override (default https://sqs.<region>.amazonaws.com)"},
		{Key: cfgSNSEndpoint, Type: sdk.FieldString, Description: "SNS endpoint base URL override (default https://sns.<region>.amazonaws.com)"},
		{Key: cfgEvBridgeEndpt, Type: sdk.FieldString, Description: "EventBridge endpoint base URL override (default https://events.<region>.amazonaws.com)"},

		{Key: cfgProject, Type: sdk.FieldString, Description: "GCP project id (gcp)"},
		{Key: cfgAccessToken, Type: sdk.FieldString, Secret: true, Description: "GCP OAuth2 bearer access token (gcp)"},
		{Key: cfgPubSubEndpoint, Type: sdk.FieldString, Description: "Pub/Sub endpoint base URL override (default https://pubsub.googleapis.com)"},

		{Key: cfgOTel, Type: sdk.FieldBool, Default: "false", Description: "opt-in OTel messaging-semconv instrumentation (default off)"},
		{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-request HTTP timeout"},
	}
}

// loadConfig resolves the connector configuration, applies defaults and AWS
// credential env fallbacks, derives the default endpoints, and validates the
// provider and its required inputs. It returns a config error so Open surfaces it
// before Gather/Notify (the SDK contract). Secret values are read here, into
// memory.
func loadConfig(cfg sdk.Config) (config, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Get(cfgProvider)))
	if provider == "" {
		return config{}, fmt.Errorf("cloudqueue: %q is required (\"aws\" or \"gcp\")", cfgProvider)
	}
	if provider != providerAWS && provider != providerGCP {
		return config{}, fmt.Errorf("cloudqueue: unsupported %s %q (want \"aws\" or \"gcp\")", cfgProvider, provider)
	}

	c := config{
		provider:     provider,
		region:       firstNonEmpty(strings.TrimSpace(cfg.Get(cfgRegion)), defaultRegion),
		accountID:    strings.TrimSpace(cfg.Get(cfgAccountID)),
		enableSQS:    cfg.GetBool(cfgEnableSQS, true),
		enableSNS:    cfg.GetBool(cfgEnableSNS, true),
		enableEvB:    cfg.GetBool(cfgEnableEvBridge, true),
		sqsEndpoint:  strings.TrimSpace(cfg.Get(cfgSQSEndpoint)),
		snsEndpoint:  strings.TrimSpace(cfg.Get(cfgSNSEndpoint)),
		evbEndpoint:  strings.TrimSpace(cfg.Get(cfgEvBridgeEndpt)),
		project:      strings.TrimSpace(cfg.Get(cfgProject)),
		accessToken:  strings.TrimSpace(cfg.Get(cfgAccessToken)),
		egressTarget: strings.TrimSpace(cfg.Get(cfgEgressTarget)),
		egressSource: firstNonEmpty(strings.TrimSpace(cfg.Get(cfgEgressSource)), defaultEgressSource),
		otel:         cfg.GetBool(cfgOTel, false),
		timeout:      cfg.GetDuration(cfgTimeout, defaultTimeout),
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}

	c.creds = awssig.Creds{
		AKID:   firstNonEmpty(strings.TrimSpace(cfg.Get(cfgAccessKeyID)), strings.TrimSpace(os.Getenv(envAccessKeyID))),
		Secret: firstNonEmpty(cfg.Get(cfgSecretAccessKey), os.Getenv(envSecretAccessKey)),
		Token:  firstNonEmpty(cfg.Get(cfgSessionToken), os.Getenv(envSessionToken)),
	}

	// Derive default service endpoints from the region when not overridden.
	if c.snsEndpoint == "" {
		c.snsEndpoint = fmt.Sprintf(defaultSNSEndpointTmpl, c.region)
	}
	if c.sqsEndpoint == "" {
		c.sqsEndpoint = fmt.Sprintf(defaultSQSEndpointTmpl, c.region)
	}
	if c.evbEndpoint == "" {
		c.evbEndpoint = fmt.Sprintf(defaultEvBridgeEndptTmpl, c.region)
	}
	c.pubsubEndpoint = firstNonEmpty(strings.TrimSpace(cfg.Get(cfgPubSubEndpoint)), defaultPubSubEndpoint)

	return c, nil
}

// validateSource checks the Source's per-provider required inputs. Credentials are
// required only when at least one service that needs them is enabled; a fully
// disabled AWS connector is a valid no-op (the connectors/aws precedent).
func (c config) validateSource() error {
	switch c.provider {
	case providerAWS:
		if (c.enableSQS || c.enableSNS || c.enableEvB) && (c.creds.AKID == "" || c.creds.Secret == "") {
			return fmt.Errorf("cloudqueue(aws): missing credentials (set %q/%q or %s/%s)",
				cfgAccessKeyID, cfgSecretAccessKey, envAccessKeyID, envSecretAccessKey)
		}
	case providerGCP:
		if c.project == "" {
			return fmt.Errorf("cloudqueue(gcp): %q is required", cfgProject)
		}
		if c.accessToken == "" {
			return fmt.Errorf("cloudqueue(gcp): %q is required", cfgAccessToken)
		}
	}
	return nil
}

// validateOutput checks the Output's per-provider required inputs: a target plus
// the credentials needed to publish to it.
func (c config) validateOutput() error {
	if c.egressTarget == "" {
		return fmt.Errorf("cloudqueue egress: %q is required", cfgEgressTarget)
	}
	switch c.provider {
	case providerAWS:
		if c.creds.AKID == "" || c.creds.Secret == "" {
			return fmt.Errorf("cloudqueue egress(aws): missing credentials (set %q/%q or %s/%s)",
				cfgAccessKeyID, cfgSecretAccessKey, envAccessKeyID, envSecretAccessKey)
		}
	case providerGCP:
		if c.project == "" {
			return fmt.Errorf("cloudqueue egress(gcp): %q is required", cfgProject)
		}
		if c.accessToken == "" {
			return fmt.Errorf("cloudqueue egress(gcp): %q is required", cfgAccessToken)
		}
	}
	return nil
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
