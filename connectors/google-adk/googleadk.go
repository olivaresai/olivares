// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package googleadk is the Olivares AI governance connector for agents built with
// Google's Agent Development Kit (ADK) 2.0 — the graph-based Workflow Runtime with
// the Task API and formal collaborative multi-agent modes (Python 2.0 GA 2026-05-19,
// Go 2.0 GA 2026-06-30). It governs agents BUILT WITH the ADK framework; the Google
// Agent Platform (reasoningEngines / Agent Registry / Model Armor) is a separate
// surface (the google-agent connector) — this one correlates with it, never
// duplicates it.
//
// WHAT IT READS (read-only, minimal-data): exported ADK 2.0 Session JSON from an
// operator-configured directory — the documented {id, app_name, user_id, state,
// events[{author, content{parts[{function_call{name}}]}, actions{state_delta,
// transfer_to_agent, escalate}, error_code}]} schema. It maps agent/app identity,
// tool (function-call) NAMES, transfers, and state-change/error COUNTS only — never
// message text, prompts, completions or tool arguments.
//
// WHAT IT GOVERNS: an agent INVENTORY (each app_name is a first-class agent, with
// its sub-agents, users, tools and Vertex correlation), EXECUTION tracking
// (an internal design note (not shipped) per agent), an APPROVED-TOOL policy (a tool a
// session invoked outside the allowlist is a drift finding), and access-map EDGES
// (agent→tool, agent→transferred-agent).
//
// HONEST BLIND SPOTS: there is no live ADK deployment in this connector — it reads
// EXPORTED sessions (the operator points it at the SessionService export / a
// deployment's session store). It cannot see an agent that has never run, and it
// reads the model an agent used only if the export carries it (the Session schema
// does not, so model governance stays with the provider connectors). It imports only
// the SDK and connectors/internal, never the engine (/core).
package googleadk

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.google-adk"

const (
	subjectInventory = "google_adk.agent"
	subjectPolicy    = "google_adk.tool_policy"
	subjectExecution = "google_adk.execution"

	resourceTool  = "google_adk.tool"
	resourceAgent = "google_adk.agent"

	// maxSessionFiles / maxSessionBytes bound the raw export scan (file count + bytes
	// read per file) so a large or hostile export cannot drag discovery into unbounded
	// I/O. But raw bytes alone do not bound the number of distinct aggregation keys a
	// hostile export can synthesize, and each distinct app / tool / transfer maps to a
	// finding or an edge — so maxDistinctApps and maxDistinctKeys additionally cap the
	// cardinality of the aggregation itself (distinct app_names, and distinct
	// agents/users/tools/transfers per app). Hitting a cap stops accumulating NEW
	// distinct keys and raises a visible truncation finding — the ceiling is never
	// silent (a truncated posture must read as truncated, not as "fully enumerated").
	maxSessionFiles = 4096
	maxSessionBytes = 8 << 20
	maxDistinctApps = 1024
	maxDistinctKeys = 2048
)

// Source is the Google ADK governance source connector (sdk.SourceConnector).
type Source struct {
	sessionDir    string
	approvedTools map[string]struct{}
	vertexEngines map[string]string // app_name -> Vertex reasoningEngine ref (correlation)
	now           func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Google ADK source with default configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "Google ADK 2.0 (agent inventory + execution tracking + tool policy)",
		Description: "Read-only governance of agents built with Google's Agent Development Kit (ADK) 2.0: reads exported ADK Session JSON (agent/app inventory, sub-agents, users, tool function-calls, transfers, state-change and error counts — never message content), an approved-tool allow policy (a tool invoked outside the allowlist is flagged), execution tracking, and correlation with Vertex reasoningEngines. " +
			"Governs agents BUILT WITH ADK; the Google Agent Platform surface is the separate google-agent connector. Offline (no session_dir) it is a no-op.",
		ConfigFields: []sdk.ConfigField{
			{Key: "session_dir", Type: sdk.FieldString, Description: "Directory of exported ADK 2.0 Session JSON files (one session object or an array per file). Empty = offline no-op."},
			{Key: "approved_tools", Type: sdk.FieldString, Description: "Optional comma-separated allowlist of approved tool (function) names. A tool a session invoked outside the list is flagged as drift."},
			{Key: "vertex_reasoning_engines", Type: sdk.FieldString, Description: "Optional comma-separated app_name=reasoningEngineRef pairs correlating an ADK app with its Vertex Agent Engine deployment (cross-reference only; no Vertex call is made)."},
		},
	}
}

// Open reads configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.sessionDir = strings.TrimSpace(cfg.Get("session_dir"))
	s.approvedTools = parseToolSet(cfg.Get("approved_tools"))
	s.vertexEngines = parseVertexEngines(cfg.Get("vertex_reasoning_engines"))
	return nil
}

// Gather reads the exported sessions and emits the ADK governance posture. Offline
// (no session_dir) it emits nothing.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.sessionDir == "" {
		return nil
	}
	sessions := s.readSessions()
	if len(sessions) == 0 {
		return nil
	}
	apps, appsTruncated := aggregate(sessions, s.approvedTools)
	at := s.clock().UTC()

	if appsTruncated {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityLow,
			SubjectKind: subjectInventory,
			SubjectRef:  "(scan)/apps-truncated",
			Title:       "ADK export exceeded the distinct-app cap (" + strconv.Itoa(maxDistinctApps) + "); some agents were not enumerated",
			DetailHash:  redact.Hash("google-adk apps-truncated cap=" + strconv.Itoa(maxDistinctApps)),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}

	appNames := make([]string, 0, len(apps))
	for name := range apps {
		appNames = append(appNames, name)
	}
	sort.Strings(appNames)

	for _, name := range appNames {
		af := apps[name]
		for _, o := range s.appObservations(af, at) {
			if err := sink.Emit(ctx, o); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// appObservations builds the findings + edges for one aggregated ADK application.
func (s *Source) appObservations(af *appFacts, at time.Time) []model.Observation {
	safeApp := textscan.SanitizeDisplay(af.AppName)
	var out []model.Observation

	// Inventory finding.
	detail := strings.Join([]string{
		"agents=" + strconv.Itoa(len(af.Agents)),
		"users=" + strconv.Itoa(len(af.Users)),
		"sessions=" + strconv.Itoa(af.Sessions),
		"events=" + strconv.Itoa(af.Events),
		"tools=" + strconv.Itoa(len(af.ToolCalls)),
		"transfers=" + strconv.Itoa(len(af.Transfers)),
		"state_writes=" + strconv.Itoa(af.StateWrites),
		"errors=" + strconv.Itoa(af.Errors),
		"escalations=" + strconv.Itoa(af.Escalations),
	}, " ")
	out = append(out, model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectInventory,
		SubjectRef:  safeApp,
		Title:       "ADK agent inventory " + quote(safeApp) + " (" + detail + ")",
		DetailHash:  redact.Hash("google-adk inventory app=" + safeApp + " " + detail),
		OccurredAt:  at,
	})

	// Vertex correlation.
	if ref, ok := s.vertexEngines[af.AppName]; ok {
		out = append(out, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityInfo,
			SubjectKind: subjectInventory,
			SubjectRef:  safeApp + "/vertex",
			Title:       "ADK agent " + quote(safeApp) + " is correlated with a Vertex reasoningEngine deployment",
			DetailHash:  redact.Hash("google-adk vertex-correlation app=" + safeApp + " engine=" + redact.Clean(ref)),
			OccurredAt:  at,
		})
	}

	// Unapproved-tool drift.
	if len(af.unapprovedTool) > 0 {
		tools := sortedKeys(af.unapprovedTool)
		safeTools := make([]string, 0, len(tools))
		for _, t := range tools {
			safeTools = append(safeTools, textscan.SanitizeDisplay(t))
		}
		out = append(out, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityHigh,
			SubjectKind: subjectPolicy,
			SubjectRef:  safeApp + "/unapproved-tools",
			Title:       "ADK agent " + quote(safeApp) + " invoked " + strconv.Itoa(len(tools)) + " tool(s) outside the approved-tool allowlist",
			DetailHash:  redact.Hash("google-adk unapproved-tools app=" + safeApp + " tools=" + strings.Join(safeTools, ",")),
			OccurredAt:  at,
		})
	}

	// Execution errors.
	if af.Errors > 0 {
		out = append(out, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectExecution,
			SubjectRef:  safeApp + "/errors",
			Title:       "ADK agent " + quote(safeApp) + " has " + strconv.Itoa(af.Errors) + " errored event(s) across its sessions",
			DetailHash:  redact.Hash("google-adk errors app=" + safeApp + " count=" + strconv.Itoa(af.Errors)),
			OccurredAt:  at,
		})
	}

	// Cardinality truncation: a dimension hit its distinct-key cap for this app, so
	// its enumeration below is partial. Surfaced (never silent) so the posture reads
	// as truncated rather than complete.
	if len(af.truncated) > 0 {
		dims := sortedKeys(af.truncated)
		out = append(out, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityLow,
			SubjectKind: subjectInventory,
			SubjectRef:  safeApp + "/truncated",
			Title:       "ADK agent " + quote(safeApp) + " exceeded the per-dimension cardinality cap (" + strconv.Itoa(maxDistinctKeys) + ") for: " + strings.Join(dims, ","),
			DetailHash:  redact.Hash("google-adk truncated app=" + safeApp + " dims=" + strings.Join(dims, ",") + " cap=" + strconv.Itoa(maxDistinctKeys)),
			OccurredAt:  at,
		})
	}

	// Edges: agent -> tool, agent -> transferred agent (config-declared, from execution).
	for _, tool := range sortedCountKeys(af.ToolCalls) {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: safeApp,
			ResourceKind: resourceTool, ResourceRef: textscan.SanitizeDisplay(tool),
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: "tool", ObservedAt: at,
		})
	}
	for _, target := range sortedCountKeys(af.Transfers) {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: safeApp,
			ResourceKind: resourceAgent, ResourceRef: textscan.SanitizeDisplay(target),
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: "transfer", ObservedAt: at,
		})
	}
	return out
}

// readSessions scans the session directory (bounded) and parses each JSON file as
// either one session object or an array of sessions. A malformed file is skipped
// (discovery never aborts on one bad export).
func (s *Source) readSessions() []adkSession {
	entries, err := os.ReadDir(s.sessionDir)
	if err != nil {
		return nil
	}
	var out []adkSession
	files := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		files++
		if files > maxSessionFiles {
			break
		}
		data, ok := readCapped(filepath.Join(s.sessionDir, e.Name()), maxSessionBytes)
		if !ok {
			continue
		}
		out = append(out, parseSessions(data)...)
	}
	return out
}

// parseSessions decodes a file as a single session object or an array of sessions.
func parseSessions(data []byte) []adkSession {
	trimmed := strings.TrimLeftFunc(string(data), func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' || r == '\r' })
	if strings.HasPrefix(trimmed, "[") {
		var arr []adkSession
		if json.Unmarshal(data, &arr) == nil {
			return arr
		}
		return nil
	}
	var one adkSession
	if json.Unmarshal(data, &one) == nil && (one.ID != "" || one.AppName != "" || len(one.Events) > 0) {
		return []adkSession{one}
	}
	return nil
}

func readCapped(path string, limit int64) ([]byte, bool) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied export path, read-only
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, false
	}
	return buf, true
}

func parseToolSet(csv string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range strings.Split(csv, ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			out[t] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseVertexEngines(csv string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(csv, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if ok && k != "" && v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func quote(s string) string { return strconv.Quote(s) }
