// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// orgcontrols.go implements the read-only OpenAI Admin API organization-control
// surfaces: spend alerts, project model permissions, hosted tool
// permissions, groups, and roles. All calls are GET-only through modelprovider.Client.
package openai

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	subjectSpendAlert           = "openai.spend_alert"
	subjectModelPermissions     = "openai.model_permissions"
	subjectHostedToolPermission = "openai.hosted_tool_permissions"
	subjectAgentKitGovernance   = "openai.agentkit_governance"
	subjectGroups               = "openai.groups"
	subjectRole                 = "openai.role"
)

// fetchNonArchivedProjects lists OpenAI projects once, then filters archived
// projects for per-project Admin API surfaces.
func (s *Source) fetchNonArchivedProjects(ctx context.Context) ([]modelprovider.WorkspaceRef, error) {
	projects, err := s.fetchProjects(ctx)
	if err != nil {
		return nil, err
	}
	return nonArchivedProjects(projects), nil
}

func nonArchivedProjects(projects []modelprovider.WorkspaceRef) []modelprovider.WorkspaceRef {
	out := make([]modelprovider.WorkspaceRef, 0, len(projects))
	for _, p := range projects {
		if !p.Archived {
			out = append(out, p)
		}
	}
	return out
}

// gatherSpendAlerts reads organization and project spend alerts.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
func (s *Source) gatherSpendAlerts(ctx context.Context, sink sdk.Sink) error {
	projects, err := s.fetchNonArchivedProjects(ctx)
	return s.gatherSpendAlertsForProjects(ctx, sink, projects, err)
}

// gatherSpendAlertsForProjects reads spend alerts using a caller-supplied project
// list so Gather reuses one project fetch across new per-project surfaces.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
func (s *Source) gatherSpendAlertsForProjects(ctx context.Context, sink sdk.Sink, projects []modelprovider.WorkspaceRef, projectErr error) error {
	orgAlerts, err := s.collectSpendAlerts(ctx, "/v1/organization/spend_alerts")
	if err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.unavailableFinding("spend_alerts", "/v1/organization/spend_alerts"))
		}
		return err
	}
	orgCount := len(orgAlerts)
	for _, alert := range orgAlerts {
		if alert.ID == "" {
			continue
		}
		if err := sink.Emit(ctx, s.spendAlertFinding(alert, "organization", "")); err != nil {
			return err
		}
	}

	if projectErr != nil {
		if isUnavailable(projectErr) {
			return sink.Emit(ctx, s.unavailableFinding("spend_alerts", "/v1/organization/projects"))
		}
		return projectErr
	}

	projectCount := 0
	for _, p := range projects {
		path := "/v1/organization/projects/" + p.ID + "/spend_alerts"
		alerts, err := s.collectSpendAlerts(ctx, path)
		if err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("spend_alerts", path))
			}
			return err
		}
		projectCount += len(alerts)
		for _, alert := range alerts {
			if alert.ID == "" {
				continue
			}
			if err := sink.Emit(ctx, s.spendAlertFinding(alert, p.ID, p.Name)); err != nil {
				return err
			}
		}
	}

	return sink.Emit(ctx, s.spendAlertsSummaryFinding(orgCount, projectCount, len(projects)))
}

func (s *Source) collectSpendAlerts(ctx context.Context, path string) ([]spendAlertEntry, error) {
	var out []spendAlertEntry
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp spendAlertsResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after", after)
		}
		if err := s.client.GetJSON(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

func (s *Source) spendAlertFinding(alert spendAlertEntry, scopeRef, scopeName string) model.FindingReport {
	scope := "org-level"
	subjectRef := "organization/" + alert.ID
	if scopeRef != "organization" {
		scope = "project " + scopeRef
		if scopeName != "" {
			scope = "project " + scopeName
		}
		subjectRef = scopeRef + "/" + alert.ID
	}
	interval := alert.Interval
	if interval == "" {
		interval = "interval unknown"
	}
	title := fmt.Sprintf("Spend alert %s - %s, threshold %s/%s",
		alert.ID, scope, formatSpendThreshold(alert.Currency, alert.ThresholdAmount), interval)
	detail := fmt.Sprintf("openai spend_alert id=%s object=%s scope=%s threshold_cents=%d currency=%s interval=%s channel_type=%s recipients=%s subject_prefix=%s",
		alert.ID, alert.Object, scopeRef, alert.ThresholdAmount, alert.Currency, alert.Interval,
		alert.NotificationChannel.Type, strings.Join(sortedStrings(alert.NotificationChannel.Recipients), ","), alert.NotificationChannel.SubjectPrefix)

	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectSpendAlert,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) spendAlertsSummaryFinding(orgCount, projectCount, projectTotal int) model.FindingReport {
	total := orgCount + projectCount
	sev := model.SeverityInfo
	title := fmt.Sprintf("OpenAI spend alerts configured: %d org-level, %d project-level across %d non-archived project(s)",
		orgCount, projectCount, projectTotal)
	if total == 0 {
		sev = model.SeverityLow
		title = "No OpenAI spend alerts are configured (budget governance gap)"
	}
	detail := fmt.Sprintf("openai spend_alerts org=%d project=%d projects=%d total=%d", orgCount, projectCount, projectTotal, total)
	return model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectSpendAlert,
		SubjectRef:  "organization",
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func formatSpendThreshold(currency string, cents int64) string {
	major := float64(cents) / 100
	if currency == "" || strings.EqualFold(currency, "USD") {
		return fmt.Sprintf("$%.2f", major)
	}
	return fmt.Sprintf("%.2f %s", major, currency)
}

// gatherModelPermissions reads per-project model permission singletons. Permitted
// vs observed comparison against Olivares model-access policy lives in AGPL modules,
// not in this Apache connector.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
func (s *Source) gatherModelPermissions(ctx context.Context, sink sdk.Sink) error {
	projects, err := s.fetchNonArchivedProjects(ctx)
	return s.gatherModelPermissionsForProjects(ctx, sink, projects, err)
}

// gatherModelPermissionsForProjects reads model permissions using a shared project
// list supplied by Gather.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
func (s *Source) gatherModelPermissionsForProjects(ctx context.Context, sink sdk.Sink, projects []modelprovider.WorkspaceRef, projectErr error) error {
	if projectErr != nil {
		if isUnavailable(projectErr) {
			return sink.Emit(ctx, s.unavailableFinding("model_permissions", "/v1/organization/projects"))
		}
		return projectErr
	}
	configured := 0
	for _, p := range projects {
		path := "/v1/organization/projects/" + p.ID + "/model_permissions"
		var resp modelPermissionsResponse
		if err := s.client.GetJSON(ctx, path, nil, &resp); err != nil {
			switch apiStatusCode(err) {
			case httpStatusNotFound:
				continue
			case httpStatusForbidden:
				return sink.Emit(ctx, s.unavailableFinding("model_permissions", path))
			}
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("model_permissions", path))
			}
			return err
		}
		configured++
		if err := sink.Emit(ctx, s.modelPermissionsFinding(p, resp)); err != nil {
			return err
		}
	}
	return sink.Emit(ctx, s.modelPermissionsSummaryFinding(configured, len(projects)))
}

func (s *Source) modelPermissionsFinding(project modelprovider.WorkspaceRef, perms modelPermissionsResponse) model.FindingReport {
	mode := perms.Mode
	if mode == "" {
		mode = "unknown"
	}
	scope := project.Name
	if scope == "" {
		scope = project.ID
	}
	modelIDs := sortedStrings(perms.ModelIDs)
	title := fmt.Sprintf("OpenAI project %q model permissions: %s, %d model(s)", scope, mode, len(modelIDs))
	detail := fmt.Sprintf("openai model_permissions project=%s mode=%s model_ids=%s", project.ID, mode, strings.Join(modelIDs, ","))
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectModelPermissions,
		SubjectRef:  project.ID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) modelPermissionsSummaryFinding(configured, totalProjects int) model.FindingReport {
	sev := model.SeverityInfo
	if configured == 0 {
		sev = model.SeverityLow
	}
	title := fmt.Sprintf("%d of %d non-archived projects have model permissions configured", configured, totalProjects)
	detail := fmt.Sprintf("openai model_permissions configured=%d projects=%d", configured, totalProjects)
	return model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectModelPermissions,
		SubjectRef:  "organization",
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// gatherHostedToolPermissions reads per-project hosted tool permission singletons.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
func (s *Source) gatherHostedToolPermissions(ctx context.Context, sink sdk.Sink) error {
	projects, err := s.fetchNonArchivedProjects(ctx)
	return s.gatherHostedToolPermissionsForProjects(ctx, sink, projects, err)
}

// gatherHostedToolPermissionsForProjects reads hosted tool permissions using a
// shared project list supplied by Gather.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
func (s *Source) gatherHostedToolPermissionsForProjects(ctx context.Context, sink sdk.Sink, projects []modelprovider.WorkspaceRef, projectErr error) error {
	if projectErr != nil {
		if isUnavailable(projectErr) {
			return sink.Emit(ctx, s.unavailableFinding("hosted_tool_permissions", "/v1/organization/projects"))
		}
		return projectErr
	}
	configured := 0
	mcpEnabled := 0
	for _, p := range projects {
		path := "/v1/organization/projects/" + p.ID + "/hosted_tool_permissions"
		var resp hostedToolPermissionsResponse
		if err := s.client.GetJSON(ctx, path, nil, &resp); err != nil {
			switch apiStatusCode(err) {
			case httpStatusNotFound:
				continue
			case httpStatusForbidden:
				return sink.Emit(ctx, s.unavailableFinding("hosted_tool_permissions", path))
			}
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("hosted_tool_permissions", path))
			}
			return err
		}
		configured++
		if resp.MCP.Enabled {
			mcpEnabled++
		}
		if err := sink.Emit(ctx, s.hostedToolPermissionsFinding(p, resp)); err != nil {
			return err
		}
	}
	return sink.Emit(ctx, s.hostedToolPermissionsSummaryFinding(configured, len(projects), mcpEnabled))
}

func (s *Source) hostedToolPermissionsFinding(project modelprovider.WorkspaceRef, perms hostedToolPermissionsResponse) model.FindingReport {
	scope := project.Name
	if scope == "" {
		scope = project.ID
	}
	enabled := enabledHostedTools(perms)
	title := fmt.Sprintf("OpenAI project %q hosted tools enabled: %s", scope, strings.Join(enabled, ", "))
	detail := fmt.Sprintf("openai hosted_tool_permissions project=%s file_search=%v web_search=%v image_generation=%v mcp=%v code_interpreter=%v",
		project.ID, perms.FileSearch.Enabled, perms.WebSearch.Enabled, perms.ImageGeneration.Enabled, perms.MCP.Enabled, perms.CodeInterpreter.Enabled)
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectHostedToolPermission,
		SubjectRef:  project.ID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func enabledHostedTools(perms hostedToolPermissionsResponse) []string {
	enabled := make([]string, 0, 5)
	if perms.FileSearch.Enabled {
		enabled = append(enabled, "file_search")
	}
	if perms.WebSearch.Enabled {
		enabled = append(enabled, "web_search")
	}
	if perms.ImageGeneration.Enabled {
		enabled = append(enabled, "image_generation")
	}
	if perms.MCP.Enabled {
		enabled = append(enabled, "mcp")
	}
	if perms.CodeInterpreter.Enabled {
		enabled = append(enabled, "code_interpreter")
	}
	if len(enabled) == 0 {
		return []string{"none"}
	}
	return enabled
}

func (s *Source) hostedToolPermissionsSummaryFinding(configured, totalProjects, mcpEnabled int) model.FindingReport {
	title := fmt.Sprintf("OpenAI hosted tool permissions: %d of %d non-archived projects configured, %d with MCP enabled",
		configured, totalProjects, mcpEnabled)
	detail := fmt.Sprintf("openai hosted_tool_permissions configured=%d projects=%d mcp_enabled=%d",
		configured, totalProjects, mcpEnabled)
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectHostedToolPermission,
		SubjectRef:  "organization",
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// gatherGroups reads organization groups and emits one posture summary.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
func (s *Source) gatherGroups(ctx context.Context, sink sdk.Sink) error {
	total := 0
	scimManaged := 0
	tenantGroups := 0
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp orgGroupsResponse
		q := url.Values{"limit": {"1000"}}
		if after != "" {
			q.Set("after", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/organization/groups", q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("groups", "/v1/organization/groups"))
			}
			return err
		}
		for _, group := range resp.Data {
			total++
			if group.IsSCIMManaged {
				scimManaged++
			}
			if group.GroupType == "tenant_group" {
				tenantGroups++
			}
		}
		if !resp.HasMore || resp.Next == "" {
			break
		}
		after = resp.Next
	}
	return sink.Emit(ctx, s.groupsSummaryFinding(total, scimManaged, tenantGroups))
}

func (s *Source) groupsSummaryFinding(total, scimManaged, tenantGroups int) model.FindingReport {
	title := fmt.Sprintf("OpenAI groups: %d total, %d SCIM-managed, %d tenant_group", total, scimManaged, tenantGroups)
	detail := fmt.Sprintf("openai groups total=%d scim_managed=%d tenant_group=%d", total, scimManaged, tenantGroups)
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectGroups,
		SubjectRef:  "organization",
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// gatherRoles reads organization roles and emits one summary plus custom-role
// inventory findings.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
func (s *Source) gatherRoles(ctx context.Context, sink sdk.Sink) error {
	var roles []orgRoleEntry
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp orgRolesResponse
		q := url.Values{"limit": {"1000"}}
		if after != "" {
			q.Set("after", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/organization/roles", q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("roles", "/v1/organization/roles"))
			}
			return err
		}
		roles = append(roles, resp.Data...)
		if !resp.HasMore || resp.Next == "" {
			break
		}
		after = resp.Next
	}
	predefined := 0
	custom := 0
	for _, role := range roles {
		if role.PredefinedRole {
			predefined++
		} else {
			custom++
		}
	}
	if err := sink.Emit(ctx, s.rolesSummaryFinding(len(roles), predefined, custom)); err != nil {
		return err
	}
	for _, role := range roles {
		if role.PredefinedRole || role.ID == "" {
			continue
		}
		if err := sink.Emit(ctx, s.customRoleFinding(role)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) rolesSummaryFinding(total, predefined, custom int) model.FindingReport {
	title := fmt.Sprintf("OpenAI roles: %d total, %d predefined, %d custom", total, predefined, custom)
	detail := fmt.Sprintf("openai roles total=%d predefined=%d custom=%d", total, predefined, custom)
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectRole,
		SubjectRef:  "organization",
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) customRoleFinding(role orgRoleEntry) model.FindingReport {
	perms := sortedStrings(role.Permissions)
	title := fmt.Sprintf("OpenAI custom role %q (%s): %d permission(s)", role.Name, role.ResourceType, len(perms))
	detail := fmt.Sprintf("openai custom_role id=%s name=%s resource_type=%s permissions=%s description=%s",
		role.ID, role.Name, role.ResourceType, strings.Join(perms, ","), role.Description)
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectRole,
		SubjectRef:  role.ID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// gatherAgentKitPosture emits one HONEST structural finding about the AgentKit
// governance surface (E4). VERIFIED 2026-07-20 against developers.openai.com: the
// AgentKit "Connector Registry" and "Global Admin Console" are CONSOLE-ONLY — the
// Administration API exposes no connectors/connector_registry/global_admin/agents
// resource, so which named connectors an org's agents may use, and the Global-Owner
// domain/SSO/multi-org controls, are NOT programmatically observable (same honest
// degradation posture the connector takes elsewhere — we say so rather than fabricate a
// poll of an endpoint that does not exist). The one API-observable connector-governance
// lever IS covered: hosted_tool_permissions (GET+POST per project) with its `mcp`
// (Model Context Protocol / connectors) enable toggle — see gatherHostedToolPermissions.
// It runs only with an admin key on the admin surface (composed by Gather's admin block).
func (s *Source) gatherAgentKitPosture(_ context.Context, sink sdk.Sink) error {
	detail := "openai agentkit governance: connector_registry=console-only(no-api); " +
		"global_admin_console=console-only(no-api; domains/sso/multi-org Global-Owner UI); " +
		"api_observable_lever=hosted_tool_permissions(per-project; mcp toggle governs MCP/connectors); " +
		"model_permissions=allow_list|deny_list(api); verified=2026-07-20"
	return sink.Emit(context.Background(), model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAgentKitGovernance,
		SubjectRef:  "organization",
		Title: "AgentKit Connector Registry and Global Admin Console are Console-only — no programmatic admin API " +
			"(per-project connector/tool governance IS API-observable via hosted_tool_permissions, incl. the mcp toggle).",
		DetailHash: redact.Hash(detail),
		OccurredAt: s.clock().UTC(),
	})
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

const (
	httpStatusForbidden = 403
	httpStatusNotFound  = 404
)

func apiStatusCode(err error) int {
	if err == nil {
		return 0
	}
	var apiErr *modelprovider.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	msg := err.Error()
	for _, status := range []int{403, 404, 410} {
		if strings.Contains(msg, "status "+strconv.Itoa(status)) {
			return status
		}
	}
	return 0
}
