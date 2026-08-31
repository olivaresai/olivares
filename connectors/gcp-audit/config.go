// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcpaudit

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.gcp-audit"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgCredentialsJSON       = "credentials_json"
	cfgCredentialsFile       = "credentials_file"
	cfgAccessToken           = "access_token"
	cfgOrganizationID        = "organization_id"
	cfgProjects              = "projects"
	cfgEnableInventory       = "enable_inventory"
	cfgEnableAudit           = "enable_audit"
	cfgEnableServiceAccounts = "enable_service_accounts"
	cfgLookback              = "lookback"
	cfgMaxEvents             = "max_events"
	cfgMaxPages              = "max_pages"
	cfgSharedAccounts        = "shared_accounts"
	cfgLogFilter             = "log_filter"
	cfgCRMEndpoint           = "crm_endpoint"
	cfgIAMEndpoint           = "iam_endpoint"
	cfgLoggingEndpoint       = "logging_endpoint"
	cfgTokenURI              = "token_uri"
	cfgTimeout               = "timeout"
)

// Defaults.
const (
	defaultCRMEndpoint     = "https://cloudresourcemanager.googleapis.com"
	defaultIAMEndpoint     = "https://iam.googleapis.com"
	defaultLoggingEndpoint = "https://logging.googleapis.com"
	defaultLookback        = time.Hour
	defaultMaxEvents       = 1000
	defaultMaxPages        = 50
	defaultTimeout         = 30 * time.Second
	// maxFolderDepth bounds the Resource Manager hierarchy walk so a pathological
	// (or cyclic, which GCP forbids but we never trust) tree cannot loop forever.
	maxFolderDepth = 8
)

// auditTypeFilter selects every Cloud Audit Logs entry (Admin Activity, Data
// Access, System Event, Policy Denied) by its protoPayload type. The per-entry
// logName then tells which category it is (categoryFromLogName). It is the
// default log_filter; an operator may override it to narrow the pull.
const auditTypeFilter = `protoPayload.@type="type.googleapis.com/google.cloud.audit.AuditLog"`

// config is the resolved connector configuration. The credential fields hold
// secret values in memory only; they are never logged or emitted.
type config struct {
	tokens tokenSource // nil ⇒ offline (no credential): Gather is a silent no-op.

	orgID    string   // bare org number ("123456789012"), or "" if not configured.
	projects []string // explicit project ids (in addition to any discovered by the org walk).

	enableInventory       bool
	enableAudit           bool
	enableServiceAccounts bool

	crmEndpoint     string
	iamEndpoint     string
	loggingEndpoint string

	logFilter string
	shared    identity.SharedSet

	lookback  time.Duration
	maxEvents int
	maxPages  int
	timeout   time.Duration
}

// auditResourceNames returns the Cloud Logging resourceNames to read audit logs
// from: the organization (when configured) and every explicit project. An empty
// result means audit cannot run (no scope), which Open guards against.
func (c config) auditResourceNames() []string {
	var rs []string
	if c.orgID != "" {
		rs = append(rs, "organizations/"+c.orgID)
	}
	for _, p := range c.projects {
		rs = append(rs, "projects/"+p)
	}
	return rs
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "GCP Cloud Audit Logs + Resource Manager",
		Description: "Read-only org-level GCP management plane: Resource Manager/IAM inventory (org/folder/project/service-account topology) and Cloud Audit Logs (Admin Activity + Data Access) control-plane activity. Emits topology and identity→gcp.api edges; never reads payloads, secrets or key material.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgCredentialsJSON, Type: sdk.FieldString, Secret: true, Description: "service-account key JSON (inline). Empty + no credentials_file/access_token ⇒ offline (no-op)."},
			{Key: cfgCredentialsFile, Type: sdk.FieldString, Description: "path to a service-account key JSON file (used when credentials_json is empty)."},
			{Key: cfgAccessToken, Type: sdk.FieldString, Secret: true, Description: "pre-issued OAuth2 access token (WIF/ADC sidecar). Overrides the service-account key when set."},
			{Key: cfgOrganizationID, Type: sdk.FieldString, Description: `GCP organization id ("123456789012" or "organizations/123456789012"). Enables the org hierarchy walk and org-scoped audit.`},
			{Key: cfgProjects, Type: sdk.FieldString, Description: "comma-separated project ids to inventory service accounts for and read audit logs from (in addition to any discovered via the org)."},
			{Key: cfgEnableInventory, Type: sdk.FieldBool, Default: "true", Description: "discover org/folder/project hierarchy + service accounts (Resource Manager + IAM)."},
			{Key: cfgEnableAudit, Type: sdk.FieldBool, Default: "true", Description: "read Cloud Audit Logs (Admin Activity + Data Access) as control-plane activity."},
			{Key: cfgEnableServiceAccounts, Type: sdk.FieldBool, Default: "true", Description: "include per-project service-account inventory (requires IAM serviceAccounts.list)."},
			{Key: cfgLookback, Type: sdk.FieldDuration, Default: defaultLookback.String(), Description: "Cloud Audit Logs lookback window."},
			{Key: cfgMaxEvents, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxEvents), Description: "max audit log entries per pass."},
			{Key: cfgMaxPages, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxPages), Description: "max API pages per list operation (pagination safety bound)."},
			{Key: cfgSharedAccounts, Type: sdk.FieldString, Description: "comma-separated principalEmails that are shared/pooled (attribution marked approximate)."},
			{Key: cfgLogFilter, Type: sdk.FieldString, Description: "advanced: Cloud Logging filter override — must be valid filter syntax (the connector ANDs a `timestamp >=` window onto it). Defaults to all google.cloud.audit.AuditLog entries."},
			{Key: cfgCRMEndpoint, Type: sdk.FieldString, Default: defaultCRMEndpoint, Description: "Cloud Resource Manager endpoint base URL (override for testing)."},
			{Key: cfgIAMEndpoint, Type: sdk.FieldString, Default: defaultIAMEndpoint, Description: "IAM endpoint base URL (override for testing)."},
			{Key: cfgLoggingEndpoint, Type: sdk.FieldString, Default: defaultLoggingEndpoint, Description: "Cloud Logging endpoint base URL (override for testing)."},
			{Key: cfgTokenURI, Type: sdk.FieldString, Description: "OAuth2 token endpoint override (defaults to the key's token_uri or oauth2.googleapis.com/token)."},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-request HTTP timeout."},
		},
	}
}

// loadConfig resolves the connector configuration, applying defaults, parsing the
// credential (inline JSON, file, or a pre-issued token), and validating that a
// scope (org or projects) is present for each enabled service. A MISSING
// credential is offline-safe (no token source ⇒ Gather is a no-op), mirroring the
// other live GCP/Azure connectors; a MALFORMED credential or unreadable file is a
// configuration error surfaced here, before Gather. Secret values are read here
// and held in memory only.
func loadConfig(cfg sdk.Config, client *http.Client) (config, error) {
	c := config{
		orgID:                 normalizeOrgID(cfg.Get(cfgOrganizationID)),
		projects:              splitCSV(cfg.Get(cfgProjects)),
		enableInventory:       cfg.GetBool(cfgEnableInventory, true),
		enableAudit:           cfg.GetBool(cfgEnableAudit, true),
		enableServiceAccounts: cfg.GetBool(cfgEnableServiceAccounts, true),
		crmEndpoint:           firstNonEmpty(strings.TrimSpace(cfg.Get(cfgCRMEndpoint)), defaultCRMEndpoint),
		iamEndpoint:           firstNonEmpty(strings.TrimSpace(cfg.Get(cfgIAMEndpoint)), defaultIAMEndpoint),
		loggingEndpoint:       firstNonEmpty(strings.TrimSpace(cfg.Get(cfgLoggingEndpoint)), defaultLoggingEndpoint),
		logFilter:             firstNonEmpty(strings.TrimSpace(cfg.Get(cfgLogFilter)), auditTypeFilter),
		shared:                identity.ParseSharedAccounts(cfg.Get(cfgSharedAccounts)),
		lookback:              cfg.GetDuration(cfgLookback, defaultLookback),
		maxEvents:             cfg.GetInt(cfgMaxEvents, defaultMaxEvents),
		maxPages:              cfg.GetInt(cfgMaxPages, defaultMaxPages),
		timeout:               cfg.GetDuration(cfgTimeout, defaultTimeout),
	}
	if c.lookback <= 0 {
		c.lookback = defaultLookback
	}
	if c.maxEvents <= 0 {
		c.maxEvents = defaultMaxEvents
	}
	if c.maxPages <= 0 {
		c.maxPages = defaultMaxPages
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}

	ts, err := resolveTokenSource(cfg, client)
	if err != nil {
		return config{}, err
	}
	c.tokens = ts

	// A configured scope is required only when a service that needs it is enabled
	// AND a credential is present. Offline (no credential) is always valid (no-op).
	if c.tokens != nil && (c.enableInventory || c.enableAudit) && c.orgID == "" && len(c.projects) == 0 {
		return config{}, fmt.Errorf("gcp-audit: set %q (org hierarchy + org-scoped audit) or %q (project-scoped) when a service is enabled",
			cfgOrganizationID, cfgProjects)
	}
	return c, nil
}

// resolveTokenSource picks the credential: a pre-issued access_token (static),
// else an inline service-account key, else a key file. All three empty ⇒ nil
// (offline). A malformed key or unreadable file errors.
func resolveTokenSource(cfg sdk.Config, client *http.Client) (tokenSource, error) {
	if tok := strings.TrimSpace(cfg.Get(cfgAccessToken)); tok != "" {
		return staticTokenSource{tok: tok}, nil
	}
	raw := []byte(strings.TrimSpace(cfg.Get(cfgCredentialsJSON)))
	if len(raw) == 0 {
		if file := strings.TrimSpace(cfg.Get(cfgCredentialsFile)); file != "" {
			b, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("gcp-audit: read credentials_file: %w", err)
			}
			raw = b
		}
	}
	if len(raw) == 0 {
		return nil, nil // offline
	}
	return newSATokenSource(raw, client, cfg.Get(cfgTokenURI))
}

// normalizeOrgID accepts "organizations/123", "123", or a blank value and returns
// the bare org number (or "" when blank). It never fails: a malformed value
// simply yields whatever survives trimming, and the API call later reports a
// health finding rather than the connector guessing.
func normalizeOrgID(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "organizations/")
	return strings.TrimSpace(s)
}

// splitCSV trims and splits a comma-separated config value, dropping blanks.
func splitCSV(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
