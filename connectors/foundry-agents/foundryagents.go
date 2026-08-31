// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package foundryagents

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.foundry-agents"

const version = "0.1.0"

const (
	defaultManagementEndpoint      = "https://management.azure.com"
	defaultAccountsAPIVersion      = "2024-10-01"
	defaultProjectsAPIVersion      = "2025-06-01"
	defaultApplicationsAPIVersion  = "2026-05-01"
	defaultDataPlaneEnabled        = true
	defaultMaxPages                = 50
	defaultTimeout                 = 30 * time.Second
	kindFoundryAgentApplication    = "foundry_agent_application"
	kindFoundryAgent               = "foundry_agent"
	objectFoundryProject           = "foundry_project"
	objectFoundryAgentApplication  = "foundry_agent_application"
	objectFoundryAgent             = "foundry_agent"
	defaultDataPlaneAPIVersion     = "v1"
	defaultDataPlaneAccountBaseFmt = "https://%s.services.ai.azure.com"
)

var defaultAccountKinds = []string{"openai", "aiservices"}

// Source reads Microsoft Foundry agent-platform inventory. It satisfies
// sdk.SourceConnector for posture findings and identitysource.GraphProvider for
// the roster snapshot.
type Source struct {
	tenantID     string
	clientID     string
	clientSecret string // Secret: sent only to the token endpoint, never logged

	oauthTokenURL string
	subscription  string
	resourceGroup string

	managementEndpoint     string
	dataPlaneEnabled       bool
	dataPlaneBase          string
	projectsAPIVersion     string
	applicationsAPIVersion string
	maxPages               int
	timeout                time.Duration

	doer httpx.Doer       // injected transport (tests); nil => http.Client{Timeout}
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns a Foundry Agents connector with default configuration.
func New() *Source {
	return &Source{
		managementEndpoint:     defaultManagementEndpoint,
		dataPlaneEnabled:       defaultDataPlaneEnabled,
		projectsAPIVersion:     defaultProjectsAPIVersion,
		applicationsAPIVersion: defaultApplicationsAPIVersion,
		maxPages:               defaultMaxPages,
		timeout:                defaultTimeout,
	}
}

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Microsoft Foundry Agents",
		Description: "Reads Microsoft Foundry projects, agent applications, agent deployments and current Agent Service data-plane agents as a read-only identity roster, and emits ARM-derived posture findings. Minimal data: no system prompts, tools, metadata, credentials or lifecycle actuation.",
		ConfigFields: []sdk.ConfigField{
			{Key: "tenant_id", Type: sdk.FieldString, Description: "Entra tenant id (directory id). Empty or incomplete client credentials = offline (empty graph)."},
			{Key: "client_id", Type: sdk.FieldString, Description: "Entra application (client) id for ARM Reader plus Foundry User data-plane reads. Empty = offline."},
			{Key: "client_secret", Type: sdk.FieldString, Secret: true, Description: "Entra client secret reference (read-only; never persisted). Empty = offline."},
			{Key: "oauth_token_url", Type: sdk.FieldString, Description: "OAuth2 token endpoint override (defaults to login.microsoftonline.com/{tenant_id}/oauth2/v2.0/token)."},
			{Key: "subscription_id", Type: sdk.FieldString, Description: "Azure subscription id to enumerate. Empty = offline."},
			{Key: "resource_group", Type: sdk.FieldString, Description: "Optional resource group filter; when set, only Cognitive Services accounts in this group are scanned."},
			{Key: "management_endpoint", Type: sdk.FieldString, Default: defaultManagementEndpoint, Description: "Azure Resource Manager endpoint base URL (override for tests/sovereign clouds)."},
			{Key: "data_plane", Type: sdk.FieldBool, Default: "true", Description: "Read per-project Foundry Agent Service agent inventory. Per-project 403/404 is tolerated."},
			{Key: "data_plane_base", Type: sdk.FieldString, Description: "Override per-account data-plane base URL (tests/sovereign). Empty derives https://{account}.services.ai.azure.com."},
			{Key: "projects_api_version", Type: sdk.FieldString, Default: defaultProjectsAPIVersion, Description: "ARM Foundry projects api-version pin."},
			{Key: "applications_api_version", Type: sdk.FieldString, Default: defaultApplicationsAPIVersion, Description: "ARM Foundry agent applications/deployments api-version pin."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound for each ARM and data-plane list operation."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "Per-request HTTP timeout."},
		},
	}
}

// Open reads configuration. It never contacts the network; token minting belongs
// to Snapshot/Gather. Missing or partial credentials, or a missing subscription,
// put the connector in an offline state rather than failing boot.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.tenantID = strings.TrimSpace(cfg.Get("tenant_id"))
	s.clientID = strings.TrimSpace(cfg.Get("client_id"))
	s.clientSecret = cfg.Get("client_secret")
	s.oauthTokenURL = strings.TrimSpace(cfg.Get("oauth_token_url"))
	s.subscription = strings.TrimSpace(cfg.Get("subscription_id"))
	s.resourceGroup = strings.TrimSpace(cfg.Get("resource_group"))
	if v := strings.TrimRight(strings.TrimSpace(cfg.Get("management_endpoint")), "/"); v != "" {
		s.managementEndpoint = v
	}
	s.dataPlaneEnabled = cfg.GetBool("data_plane", s.dataPlaneEnabled)
	s.dataPlaneBase = strings.TrimRight(strings.TrimSpace(cfg.Get("data_plane_base")), "/")
	if v := strings.TrimSpace(cfg.Get("projects_api_version")); v != "" {
		s.projectsAPIVersion = v
	}
	if v := strings.TrimSpace(cfg.Get("applications_api_version")); v != "" {
		s.applicationsAPIVersion = v
	}
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.timeout = cfg.GetDuration("timeout", s.timeout)
	if s.timeout <= 0 {
		s.timeout = defaultTimeout
	}
	return nil
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// Snapshot reads ARM inventory and, when enabled, the Foundry Agent Service
// inventory. Offline it returns an empty graph with Source/CapturedAt set.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceFoundry, CapturedAt: s.clock().UTC()}
	if s.offline() {
		return g, nil
	}

	client, err := s.armClient(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	inv, err := s.readARM(ctx, client)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, acct := range inv {
		for _, proj := range acct.Projects {
			g.Collections = append(g.Collections, projectCollection(acct.Account, proj.Project))
			for _, app := range proj.Applications {
				if app.Application.ID == "" {
					continue
				}
				g.Identities = append(g.Identities, appIdentity(app.Application, app.Deployments))
				g.Memberships = append(g.Memberships, identitysource.Membership{
					MemberRef:     app.Application.ID,
					MemberKind:    identitysource.MemberIdentity,
					CollectionRef: app.ProjectRef,
					Source:        identitysource.SourceFoundry,
				})
			}
		}
	}

	if !s.dataPlaneEnabled {
		return g, nil
	}
	tok, err := s.dataPlaneToken(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, acct := range inv {
		for _, proj := range acct.Projects {
			agents, ok, err := s.listProjectAgents(ctx, acct.Account, proj.Project, tok)
			if err != nil {
				return identitysource.Graph{}, err
			}
			if !ok {
				continue
			}
			for _, a := range agents {
				if a.ID == "" {
					continue
				}
				ref := agentRef(acct.Account.Name, proj.Project.Name, a.ID)
				g.Identities = append(g.Identities, agentIdentity(acct.Account.Name, proj.Project.Name, ref, a))
				g.Memberships = append(g.Memberships, identitysource.Membership{
					MemberRef:     ref,
					MemberKind:    identitysource.MemberIdentity,
					CollectionRef: proj.Project.ID,
					Source:        identitysource.SourceFoundry,
				})
			}
		}
	}
	return g, nil
}

type accountInventory struct {
	Account  account
	Projects []projectInventory
}

type projectInventory struct {
	Project      project
	Applications []applicationInventory
}

type applicationInventory struct {
	ProjectRef  string
	Application application
	Deployments []agentDeployment
}

func (s *Source) readARM(ctx context.Context, client *httpx.Client) ([]accountInventory, error) {
	accounts, err := s.listAccounts(ctx, client)
	if err != nil {
		return nil, err
	}
	out := make([]accountInventory, 0, len(accounts))
	for _, acct := range accounts {
		projects, err := s.listProjects(ctx, client, acct.ID)
		if err != nil {
			return nil, err
		}
		ai := accountInventory{Account: acct, Projects: make([]projectInventory, 0, len(projects))}
		for _, p := range projects {
			apps, err := s.listApplications(ctx, client, p.ID)
			if err != nil {
				return nil, err
			}
			pi := projectInventory{Project: p, Applications: make([]applicationInventory, 0, len(apps))}
			for _, app := range apps {
				deployments, err := s.listDeployments(ctx, client, app.ID)
				if err != nil {
					return nil, err
				}
				pi.Applications = append(pi.Applications, applicationInventory{
					ProjectRef:  p.ID,
					Application: app,
					Deployments: deployments,
				})
			}
			ai.Projects = append(ai.Projects, pi)
		}
		out = append(out, ai)
	}
	return out, nil
}

func (s *Source) listAccounts(ctx context.Context, client *httpx.Client) ([]account, error) {
	q := url.Values{"api-version": {defaultAccountsAPIVersion}}
	path := "/subscriptions/" + url.PathEscape(s.subscription) + "/providers/Microsoft.CognitiveServices/accounts"
	rows, err := collectARMPages[account](ctx, client, path, q, s.maxPages)
	if err != nil {
		return nil, err
	}
	var out []account
	for _, a := range rows {
		if !s.accountKindEnabled(a.Kind) {
			continue
		}
		if s.resourceGroup != "" && !strings.EqualFold(resourceGroupFromID(a.ID), s.resourceGroup) {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *Source) listProjects(ctx context.Context, client *httpx.Client, accountID string) ([]project, error) {
	q := url.Values{"api-version": {s.projectsAPIVersion}}
	return collectARMPages[project](ctx, client, accountID+"/projects", q, s.maxPages)
}

func (s *Source) listApplications(ctx context.Context, client *httpx.Client, projectID string) ([]application, error) {
	q := url.Values{"api-version": {s.applicationsAPIVersion}}
	return collectARMPages[application](ctx, client, projectID+"/applications", q, s.maxPages)
}

func (s *Source) listDeployments(ctx context.Context, client *httpx.Client, applicationID string) ([]agentDeployment, error) {
	q := url.Values{"api-version": {s.applicationsAPIVersion}}
	return collectARMPages[agentDeployment](ctx, client, applicationID+"/agentDeployments", q, s.maxPages)
}

func collectARMPages[T any](ctx context.Context, client *httpx.Client, path string, q url.Values, maxPages int) ([]T, error) {
	var out []T
	query := q
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp armList[T]
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Value...)
		if resp.NextLink == "" {
			break
		}
		path = resp.NextLink
		query = nil
	}
	return out, nil
}

func (s *Source) listProjectAgents(ctx context.Context, acct account, p project, tok string) ([]dataPlaneAgent, bool, error) {
	base := s.dataPlaneBase
	if base == "" {
		base = dataPlaneBase(acct.Name)
	}
	client := s.dataPlaneClient(base, tok)
	path := "/api/projects/" + url.PathEscape(p.Name) + "/agents"
	q := url.Values{"api-version": {defaultDataPlaneAPIVersion}}
	var out []dataPlaneAgent
	for page := 0; page < s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		var resp agentPage
		if err := client.GetJSON(ctx, path, q, &resp); err != nil {
			var se *httpx.StatusError
			if errors.As(err, &se) && (se.Status == http.StatusForbidden || se.Status == http.StatusNotFound) {
				return nil, false, nil
			}
			return nil, false, err
		}
		out = append(out, resp.items()...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		q = url.Values{"api-version": {defaultDataPlaneAPIVersion}, "after": {resp.LastID}}
	}
	return out, true, nil
}

func projectCollection(acct account, p project) identitysource.Collection {
	return identitysource.Collection{
		Ref:         p.ID,
		Kind:        identitysource.KindGroup,
		DisplayName: p.Name,
		Source:      identitysource.SourceFoundry,
		Attributes: pruneAttrs(map[string]string{
			"object":  objectFoundryProject,
			"account": acct.Name,
		}),
	}
}

func appIdentity(app application, deployments []agentDeployment) identitysource.Identity {
	return identitysource.Identity{
		Ref:         app.ID,
		Type:        identitysource.PrincipalNHI,
		Kind:        kindFoundryAgentApplication,
		DisplayName: applicationLabel(app),
		Source:      identitysource.SourceFoundry,
		Disabled:    !app.Properties.IsEnabled,
		Attributes: pruneAttrs(map[string]string{
			"provisioning_state":           app.Properties.ProvisioningState,
			"base_url":                     app.Properties.BaseURL,
			"agents":                       appAgentNames(app.Properties.Agents),
			"entra_blueprint_client_id":    app.Properties.AgentIdentityBlueprint.ClientID,
			"entra_blueprint_principal_id": app.Properties.AgentIdentityBlueprint.PrincipalID,
			"entra_instance_client_id":     app.Properties.DefaultInstanceIdentity.ClientID,
			"entra_instance_principal_id":  app.Properties.DefaultInstanceIdentity.PrincipalID,
			"deployment_states":            deploymentStates(deployments),
			"deployment_types":             deploymentTypes(deployments),
			"object":                       objectFoundryAgentApplication,
		}),
	}
}

func agentIdentity(accountName, projectName, ref string, a dataPlaneAgent) identitysource.Identity {
	latest := a.Versions.Latest
	attrs := map[string]string{
		"account":         accountName,
		"project":         projectName,
		"version":         latest.Version,
		"created_at":      latest.createdAt(),
		"status":          latest.Status,
		"definition_kind": latest.Definition.Kind,
		"model":           latest.Definition.Model,
		"object":          objectFoundryAgent,
	}
	if latest.Draft {
		attrs["draft"] = "true"
	}
	return identitysource.Identity{
		Ref:         ref,
		Type:        identitysource.PrincipalNHI,
		Kind:        kindFoundryAgent,
		DisplayName: a.Name,
		Source:      identitysource.SourceFoundry,
		Disabled:    strings.EqualFold(a.State, "disabled"),
		Attributes:  pruneAttrs(attrs),
	}
}

func (s *Source) accountKindEnabled(kind string) bool {
	k := strings.ToLower(strings.TrimSpace(kind))
	for _, want := range defaultAccountKinds {
		if k == want {
			return true
		}
	}
	return false
}

// agentRef is composite because Foundry agent ids are not verified globally
// unique across accounts and projects.
func agentRef(accountName, projectName, agentID string) string {
	return accountName + "/" + projectName + "/" + agentID
}

func dataPlaneBase(accountName string) string {
	return strings.TrimRight(strings.Replace(defaultDataPlaneAccountBaseFmt, "%s", accountName, 1), "/")
}

func applicationLabel(app application) string {
	if app.Properties.DisplayName != "" {
		return app.Properties.DisplayName
	}
	if app.Name != "" {
		return app.Name
	}
	return resourceName(app.ID)
}

func appAgentNames(rows []appAgentRef) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.AgentName != "" {
			out = append(out, r.AgentName)
		}
	}
	return strings.Join(out, ",")
}

func deploymentStates(rows []agentDeployment) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Properties.State != "" {
			out = append(out, r.Properties.State)
		}
	}
	return strings.Join(out, ",")
}

func deploymentTypes(rows []agentDeployment) string {
	out := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, r := range rows {
		v := r.Properties.DeploymentType
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return strings.Join(out, ",")
}

func resourceGroupFromID(id string) string {
	parts := strings.Split(id, "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return ""
}

func resourceName(id string) string {
	id = strings.TrimRight(id, "/")
	if i := strings.LastIndex(id, "/"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return id
}

func (s *Source) offline() bool {
	return s.tenantID == "" || s.clientID == "" || s.clientSecret == "" || s.subscription == ""
}

func (s *Source) transport() httpx.Doer {
	if s.doer != nil {
		return s.doer
	}
	return &http.Client{Timeout: s.timeout}
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func pruneAttrs(m map[string]string) map[string]string {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
