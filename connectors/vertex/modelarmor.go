// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file reads the Google Model Armor configuration — the Google equivalent of Amazon
// Bedrock Guardrails / Azure RAI — and emits it as read-only safety_posture findings
//. It reads the per-region TEMPLATES (RAI filters + confidence, prompt-injection/
// jailbreak, malicious-URI, Sensitive-Data-Protection enforcement, and the documented
// templateMetadata posture subset) and FLOOR SETTINGS (project runtime floor plus
// org/folder conformance baselines). It is the same posture pattern bedrock/guardrails.go
// uses.
//
// Two honesty boundaries, designed in:
//   - READ-FIRST: it never calls the content-reading :sanitizeUserPrompt /
//     :sanitizeModelResponse data-plane methods (those read prompts/responses). Posture is
//     fully derived from templates.get/list + getFloorSetting (config STATE only). Read-only
//     posture reads do not consume the Model Armor per-sanitized-token meter.
//   - The Model Armor DetectionConfidenceLevel enum is INVERTED: LOW_AND_ABOVE is the
//     STRICTEST filter (catches low-confidence-and-up → blocks the MOST); HIGH is the MOST
//     PERMISSIVE (only blocks high-confidence harms). The scoring below never ranks HIGH as
//     "most secure" — a filter pinned to HIGH is flagged as a weaker setting.
//
// VERIFIED 2026-07-05 against the Model Armor v1 discovery document (revision 20260624)
// and docs pages last updated 2026-06-29: TEMPLATES live on the REGIONAL host
// modelarmor.{location}.rep.googleapis.com; FLOOR SETTINGS live on the GLOBAL host
// modelarmor.googleapis.com. Mixing them fails.
package vertex

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	serviceAIPlatform       = "AI_PLATFORM"
	serviceGoogleMCPServer  = "GOOGLE_MCP_SERVER"
	templateInspectOnly     = "INSPECT_ONLY"
	templateInspectAndBlock = "INSPECT_AND_BLOCK"
	filterAliasLegacy       = "FILTER_VERSION_ALIAS_LEGACY"
	filterAliasRetired      = "FILTER_VERSION_ALIAS_RETIRED"
)

// --- Model Armor v1 wire shapes (only the fields we read) ----------------------------

type templatesResponse struct {
	Templates     []armorTemplate `json:"templates"`
	NextPageToken string          `json:"nextPageToken"`
	Unreachable   []string        `json:"unreachable"`
}

// armorTemplate is one Model Armor template; only the filter-config STATE and
// templateMetadata subset we reason over are mapped (VERIFIED 2026-07-05 against Model
// Armor v1 discovery revision 20260624). We deliberately do NOT decode the DLP
// inspect/deidentify template contents, logTemplateOperations, modalities (image modality
// is Preview and has no posture semantics here yet), multiLanguageDetection,
// ignorePartialInvocationFailures, or operator-authored custom safety error content. The
// filterVersionSelector alias/version is read only to surface legacy/retired pins; filter
// versions v1/v2/v3 exist upstream (v3 = Latest), but this connector does not infer
// posture from raw version strings.
type armorTemplate struct {
	Name             string                `json:"name"`
	FilterConfig     armorFilters          `json:"filterConfig"`
	TemplateMetadata armorTemplateMetadata `json:"templateMetadata"`
}

type armorTemplateMetadata struct {
	EnforcementType       string `json:"enforcementType"`
	LogSanitizeOperations bool   `json:"logSanitizeOperations"`
	FilterVersionSelector struct {
		Alias   string `json:"alias"`
		Version string `json:"version"`
	} `json:"filterVersionSelector"`
}

// armorFilters is the shared filterConfig used by both templates and the floor setting.
type armorFilters struct {
	RaiSettings struct {
		RaiFilters []struct {
			FilterType      string `json:"filterType"`      // SEXUALLY_EXPLICIT|HATE_SPEECH|HARASSMENT|DANGEROUS
			ConfidenceLevel string `json:"confidenceLevel"` // LOW_AND_ABOVE(strict)…HIGH(permissive)
		} `json:"raiFilters"`
	} `json:"raiSettings"`
	PiAndJailbreakFilterSettings struct {
		FilterEnforcement string `json:"filterEnforcement"` // ENABLED|DISABLED
		ConfidenceLevel   string `json:"confidenceLevel"`
	} `json:"piAndJailbreakFilterSettings"`
	MaliciousURIFilterSettings struct {
		FilterEnforcement string `json:"filterEnforcement"`
	} `json:"maliciousUriFilterSettings"`
	SdpSettings struct {
		BasicConfig struct {
			FilterEnforcement string `json:"filterEnforcement"`
		} `json:"basicConfig"`
	} `json:"sdpSettings"`
}

type floorServiceSetting struct {
	EnableCloudLogging bool `json:"enableCloudLogging"`
	InspectOnly        bool `json:"inspectOnly"`     // detect-but-allow (weaker)
	InspectAndBlock    bool `json:"inspectAndBlock"` // detect-and-block
}

// floorSetting is GET .../locations/global/floorSetting (project/org/folder, global host).
// VERIFIED 2026-07-05 against Model Armor v1 discovery revision 20260624. We decode the
// runtime-leg booleans for fidelity, including the new GOOGLE_MCP_SERVER leg, but
// deliberately do NOT decode floorSettingMetadata: multi-language detection has no
// posture semantics this connector reasons over yet.
type floorSetting struct {
	Name                          string              `json:"name"`
	FilterConfig                  armorFilters        `json:"filterConfig"`
	EnableFloorSettingEnforcement bool                `json:"enableFloorSettingEnforcement"`
	IntegratedServices            []string            `json:"integratedServices"` // project runtime binding only
	AIPlatformFloorSetting        floorServiceSetting `json:"aiPlatformFloorSetting"`
	GoogleMcpServerFloorSetting   floorServiceSetting `json:"googleMcpServerFloorSetting"`
}

// gatherModelArmor runs the Model Armor posture pass: per configured region read the
// templates, then read the project floor setting. A list/read failure is fatal to the
// pass so the caller records ONE health finding (a gap is a signal, not silence).
func (s *Source) gatherModelArmor(ctx context.Context, sink sdk.Sink, at time.Time) error {
	for _, loc := range s.cfg.modelArmorLocations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherArmorTemplates(ctx, sink, loc, at); err != nil {
			return err
		}
	}
	if err := s.gatherArmorFloor(ctx, sink, at); err != nil {
		return err
	}
	if s.cfg.modelArmorOrg != "" {
		if err := s.gatherArmorConformanceFloor(ctx, sink, "organization", "organizations", s.cfg.modelArmorOrg, at); err != nil {
			return err
		}
	}
	for _, folder := range s.cfg.modelArmorFolders {
		if err := s.gatherArmorConformanceFloor(ctx, sink, "folder", "folders", folder, at); err != nil {
			return err
		}
	}
	return nil
}

// gatherArmorTemplates lists the templates in one region (following nextPageToken) and
// emits one posture finding per template, plus an honest partial-coverage finding when the
// listing truncated or the API reported unreachable locations (no silent caps).
func (s *Source) gatherArmorTemplates(ctx context.Context, sink sdk.Sink, loc string, at time.Time) error {
	host := strings.ReplaceAll(s.cfg.modelArmorEndpoint, "{location}", loc)
	path := "/v1/projects/" + url.PathEscape(s.cfg.project) + "/locations/" + url.PathEscape(loc) + "/templates"

	token := ""
	unreachable := false
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := url.Values{}
		if token != "" {
			q.Set("pageToken", token)
		}
		var resp templatesResponse
		if err := s.getURL(ctx, joinURL(host, path, q), &resp); err != nil {
			return err
		}
		unreachable = unreachable || len(resp.Unreachable) > 0
		for _, t := range resp.Templates {
			if err := emit(ctx, sink, armorTemplateFinding(loc, t, at)); err != nil {
				return err
			}
		}
		if resp.NextPageToken == "" {
			if unreachable {
				return emit(ctx, sink, postureFinding(model.SeverityLow, subjectArmorTemplate, s.projectRef()+"/"+loc,
					"Model Armor template posture is PARTIAL — the API reported unreachable locations in "+loc,
					"vertex.model_armor_template location="+loc+" coverage=partial unreachable=true", at))
			}
			return nil
		}
		token = resp.NextPageToken
	}
	// Stopped at the page bound with a cursor still pending — say so (no silent caps).
	return emit(ctx, sink, postureFinding(model.SeverityLow, subjectArmorTemplate, s.projectRef()+"/"+loc,
		"Model Armor template posture is PARTIAL — pagination bound reached in "+loc,
		fmt.Sprintf("vertex.model_armor_template location=%s coverage=partial pages=%d cursor_pending=true", loc, s.cfg.maxPages), at))
}

// armorTemplateFinding builds the posture finding for one template: Medium when it has no
// RAI harm filters or no prompt-injection/jailbreak enforcement (the key defenses), else
// an Info/Low summary recording which protections are present, whether any filter is
// pinned to the most-permissive HIGH confidence, and the decoded templateMetadata posture
// subset. Model Armor docs say an absent/unspecified enforcementType defaults to
// INSPECT_AND_BLOCK; keep that default in code so old fixtures stay regression-stable.
func armorTemplateFinding(loc string, t armorTemplate, at time.Time) model.FindingReport {
	rai, permissive := raiSummary(t.FilterConfig)
	pi := enforced(t.FilterConfig.PiAndJailbreakFilterSettings.FilterEnforcement)
	mal := enforced(t.FilterConfig.MaliciousURIFilterSettings.FilterEnforcement)
	sdp := enforced(t.FilterConfig.SdpSettings.BasicConfig.FilterEnforcement)
	name := templateLeaf(t.Name)
	enforcement := templateEnforcementType(t.TemplateMetadata.EnforcementType)
	filterVersion := templateFilterVersion(t.TemplateMetadata)

	var gaps []string
	sev := model.SeverityInfo
	if len(rai) == 0 {
		gaps = append(gaps, "no RAI harm filters")
		sev = model.SeverityMedium
	}
	if !pi {
		gaps = append(gaps, "prompt-injection/jailbreak not enforced")
		sev = model.SeverityMedium
	}
	if enforcement == templateInspectOnly {
		gaps = append(gaps, "inspect-only enforcement (detect-but-allow)")
		if sev == model.SeverityInfo {
			sev = model.SeverityLow
		}
	}
	if legacyOrRetiredFilterAlias(t.TemplateMetadata.FilterVersionSelector.Alias) {
		gaps = append(gaps, "legacy/retired filter version pinned")
		if sev == model.SeverityInfo {
			sev = model.SeverityLow
		}
	}
	if len(permissive) > 0 && sev == model.SeverityInfo {
		// HIGH confidence = most permissive (the verified inversion); not a hard gap, but a
		// weaker setting worth flagging.
		sev = model.SeverityLow
	}

	title := "Vertex Model Armor template " + name + " active"
	if len(gaps) > 0 {
		title = "Vertex Model Armor template " + name + " has safety-config gaps: " + strings.Join(gaps, ", ")
	} else if len(permissive) > 0 {
		title = "Vertex Model Armor template " + name + " has permissive (HIGH-confidence) filters: " + strings.Join(permissive, ", ")
	}
	detail := fmt.Sprintf("vertex.model_armor_template location=%s name=%s rai=[%s] permissive_high=[%s] prompt_injection=%t malicious_uri=%t sdp=%t enforcement=%s log_sanitize=%t filter_version=%s gaps=%s",
		loc, name, strings.Join(rai, "|"), strings.Join(permissive, "|"), pi, mal, sdp,
		enforcement, t.TemplateMetadata.LogSanitizeOperations, filterVersion, strings.Join(gaps, "|"))
	return postureFinding(sev, subjectArmorTemplate, loc+"/"+name, title, detail, at)
}

// gatherArmorFloor reads the project runtime floor setting and emits its point-in-time
// posture. Drift against declared expectations is emitted as a separate policy_drift
// finding after the posture lens, including on 404.
func (s *Source) gatherArmorFloor(ctx context.Context, sink sdk.Sink, at time.Time) error {
	path := "/v1/projects/" + url.PathEscape(s.cfg.project) + "/locations/global/floorSetting"
	var fs floorSetting
	if err := s.getURL(ctx, joinURL(s.cfg.modelArmorGlobalURL, path, nil), &fs); err != nil {
		if isStatus(err, 404) {
			if err := emit(ctx, sink, postureFinding(model.SeverityMedium, subjectArmorFloor, s.projectRef(),
				"No Model Armor floor setting configured for project",
				"vertex.model_armor_floor project="+s.cfg.project+" floor=absent; no org/project-wide minimum enforcement governs Vertex traffic", at)); err != nil {
				return err
			}
			return s.emitArmorFloorDrift(ctx, sink, floorSetting{}, false, at)
		}
		return err
	}

	if err := emit(ctx, sink, projectArmorFloorFinding(s.cfg.project, fs, at)); err != nil {
		return err
	}
	return s.emitArmorFloorDrift(ctx, sink, fs, true, at)
}

// gatherArmorConformanceFloor reads an org/folder floor as a conformance baseline. Google
// documents runtime integratedServices enforcement as project-level only, so these
// findings intentionally do not flag AI_PLATFORM binding or inspect-only runtime legs.
func (s *Source) gatherArmorConformanceFloor(ctx context.Context, sink sdk.Sink, scopeName, pathPrefix, id string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	subjectRef := pathPrefix + "/" + id
	path := "/v1/" + pathPrefix + "/" + url.PathEscape(id) + "/locations/global/floorSetting"
	var fs floorSetting
	if err := s.getURL(ctx, joinURL(s.cfg.modelArmorGlobalURL, path, nil), &fs); err != nil {
		if isStatus(err, 404) {
			return emit(ctx, sink, postureFinding(model.SeverityMedium, subjectArmorFloor, subjectRef,
				"No Model Armor floor setting configured for "+scopeName+" "+id,
				"vertex.model_armor_floor "+scopeName+"="+id+" floor=absent; no conformance baseline configured at this scope", at))
		}
		return err
	}
	return emit(ctx, sink, conformanceArmorFloorFinding(scopeName, id, subjectRef, fs, at))
}

type armorFloorSummary struct {
	bindsVertex bool
	bindsMCP    bool
	rai         []string
	permissive  []string
	pi          bool
}

func summarizeArmorFloor(fs floorSetting) armorFloorSummary {
	rai, permissive := raiSummary(fs.FilterConfig)
	return armorFloorSummary{
		bindsVertex: containsFold(fs.IntegratedServices, serviceAIPlatform),
		bindsMCP:    containsFold(fs.IntegratedServices, serviceGoogleMCPServer),
		rai:         rai,
		permissive:  permissive,
		pi:          enforced(fs.FilterConfig.PiAndJailbreakFilterSettings.FilterEnforcement),
	}
}

func projectArmorFloorFinding(project string, fs floorSetting, at time.Time) model.FindingReport {
	sum := summarizeArmorFloor(fs)

	var gaps []string
	sev := model.SeverityInfo
	switch {
	case !fs.EnableFloorSettingEnforcement:
		gaps = append(gaps, "enforcement disabled")
		sev = model.SeverityMedium
	case !sum.bindsVertex:
		gaps = append(gaps, "not bound to AI_PLATFORM (Vertex)")
		sev = model.SeverityMedium
	case fs.AIPlatformFloorSetting.InspectOnly && !fs.AIPlatformFloorSetting.InspectAndBlock:
		gaps = append(gaps, "inspect-only (detect-but-allow)")
		sev = model.SeverityLow
	}
	if sum.bindsMCP && fs.GoogleMcpServerFloorSetting.InspectOnly && !fs.GoogleMcpServerFloorSetting.InspectAndBlock {
		gaps = append(gaps, "MCP leg inspect-only (detect-but-allow)")
		if sev == model.SeverityInfo {
			sev = model.SeverityLow
		}
	}

	title := "Vertex Model Armor floor setting active and enforcing"
	if len(gaps) > 0 {
		title = "Vertex Model Armor floor setting weak: " + strings.Join(gaps, ", ")
	}
	return postureFinding(sev, subjectArmorFloor, project, title, armorFloorDetail("project", project, fs, sum, gaps), at)
}

func conformanceArmorFloorFinding(scopeName, id, subjectRef string, fs floorSetting, at time.Time) model.FindingReport {
	sum := summarizeArmorFloor(fs)
	var gaps []string
	sev := model.SeverityInfo
	if !fs.EnableFloorSettingEnforcement {
		gaps = append(gaps, "enforcement disabled")
		sev = model.SeverityMedium
	}

	title := "Vertex Model Armor " + scopeName + " conformance floor active"
	if len(gaps) > 0 {
		title = "Vertex Model Armor " + scopeName + " conformance floor weak: " + strings.Join(gaps, ", ")
	}
	return postureFinding(sev, subjectArmorFloor, subjectRef, title, armorFloorDetail(scopeName, id, fs, sum, gaps), at)
}

func armorFloorDetail(scopeName, id string, fs floorSetting, sum armorFloorSummary, gaps []string) string {
	return fmt.Sprintf("vertex.model_armor_floor %s=%s enforced=%t binds_ai_platform=%t binds_mcp=%t inspect_only=%t inspect_and_block=%t mcp_inspect_only=%t mcp_inspect_and_block=%t logging_ai_platform=%t logging_mcp=%t rai=[%s] permissive_high=[%s] prompt_injection=%t gaps=%s",
		scopeName, id, fs.EnableFloorSettingEnforcement, sum.bindsVertex, sum.bindsMCP,
		fs.AIPlatformFloorSetting.InspectOnly, fs.AIPlatformFloorSetting.InspectAndBlock,
		fs.GoogleMcpServerFloorSetting.InspectOnly, fs.GoogleMcpServerFloorSetting.InspectAndBlock,
		fs.AIPlatformFloorSetting.EnableCloudLogging, fs.GoogleMcpServerFloorSetting.EnableCloudLogging,
		strings.Join(sum.rai, "|"), strings.Join(sum.permissive, "|"), sum.pi, strings.Join(gaps, "|"))
}

func (s *Source) emitArmorFloorDrift(ctx context.Context, sink sdk.Sink, fs floorSetting, present bool, at time.Time) error {
	f, ok := s.armorFloorDrift(fs, present, at)
	if !ok {
		return nil
	}
	return emit(ctx, sink, f)
}

func (s *Source) armorFloorDrift(fs floorSetting, present bool, at time.Time) (model.FindingReport, bool) {
	if !s.floorExpectationsEnabled() {
		return model.FindingReport{}, false
	}

	var violations []string
	if !present {
		violations = append(violations, "floor absent")
	} else {
		if s.cfg.expectFloorEnforce {
			if !fs.EnableFloorSettingEnforcement {
				violations = append(violations, "enforcement disabled")
			}
			if !containsFold(fs.IntegratedServices, serviceAIPlatform) {
				violations = append(violations, "not bound to AI_PLATFORM")
			}
		}
		if s.cfg.expectFloorBlock && !fs.AIPlatformFloorSetting.InspectAndBlock {
			violations = append(violations, "inspect-only, block expected")
		}
		if s.cfg.expectFloorLogging && !fs.AIPlatformFloorSetting.EnableCloudLogging {
			violations = append(violations, "cloud logging disabled, logging expected")
		}
	}
	if len(violations) == 0 {
		return model.FindingReport{}, false
	}

	title := "Vertex Model Armor floor drifted from declared baseline: " + strings.Join(violations, ", ")
	detail := fmt.Sprintf("vertex.model_armor_floor project=%s expect_enforcement=%t expect_block=%t expect_logging=%t violations=%s",
		s.cfg.project, s.cfg.expectFloorEnforce, s.cfg.expectFloorBlock, s.cfg.expectFloorLogging, strings.Join(violations, "|"))
	return driftFinding(s.cfg.project, title, detail, at), true
}

func (s *Source) floorExpectationsEnabled() bool {
	return s.cfg.expectFloorEnforce || s.cfg.expectFloorBlock || s.cfg.expectFloorLogging
}

// raiSummary returns the sorted set of configured RAI harm-filter categories and the
// (sorted) subset pinned to the most-permissive HIGH confidence (the verified inversion: a
// category ABSENT from raiFilters is NOT enabled).
func raiSummary(fc armorFilters) (categories, permissiveHigh []string) {
	for _, f := range fc.RaiSettings.RaiFilters {
		ft := strings.TrimSpace(f.FilterType)
		if ft == "" {
			continue
		}
		categories = append(categories, ft)
		if strings.EqualFold(strings.TrimSpace(f.ConfidenceLevel), "HIGH") {
			permissiveHigh = append(permissiveHigh, ft)
		}
	}
	sort.Strings(categories)
	sort.Strings(permissiveHigh)
	return categories, permissiveHigh
}

// enforced reports whether a filterEnforcement value is ENABLED (UNSPECIFIED/DISABLED/empty
// are all "not enforced").
func enforced(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "ENABLED") }

func templateEnforcementType(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "UNSPECIFIED") || strings.EqualFold(v, "ENFORCEMENT_TYPE_UNSPECIFIED") {
		return templateInspectAndBlock
	}
	return v
}

func templateFilterVersion(md armorTemplateMetadata) string {
	if alias := strings.TrimSpace(md.FilterVersionSelector.Alias); alias != "" {
		return alias
	}
	if version := strings.TrimSpace(md.FilterVersionSelector.Version); version != "" {
		return version
	}
	return "stable-default"
}

func legacyOrRetiredFilterAlias(alias string) bool {
	alias = strings.TrimSpace(alias)
	return strings.EqualFold(alias, filterAliasLegacy) || strings.EqualFold(alias, filterAliasRetired)
}

// containsFold reports whether vals contains target (case-insensitive, trimmed).
func containsFold(vals []string, target string) bool {
	for _, v := range vals {
		if strings.EqualFold(strings.TrimSpace(v), target) {
			return true
		}
	}
	return false
}

// templateLeaf returns the last path segment of a template resource name (the template id)
// for the finding title/subject, cleaned defensively.
func templateLeaf(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return redact.Clean(name)
}
