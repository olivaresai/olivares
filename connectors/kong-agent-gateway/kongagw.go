// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kongagw

import (
	"context"
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
const Name = "olivares.kong-agent-gateway"

const (
	subjectRateLimit = "kong_aigw.rate_limit"
	subjectMCP       = "kong_aigw.mcp"
	subjectModelPol  = "kong_aigw.model_policy"
	subjectPlugin    = "kong_aigw.plugin"
	subjectInventory = "kong_aigw.gateway"

	resourceModel = "kong_aigw.model"

	maxConfigFiles = 4096
	maxConfigBytes = 16 << 20
	maxTotalBytes  = 128 << 20 // aggregate ceiling across the whole directory scan
)

// AI plugin names (lowercased) this connector recognizes.
const (
	pluginProxy     = "ai-proxy"
	pluginProxyAdv  = "ai-proxy-advanced"
	pluginRateLimit = "ai-rate-limiting-advanced"
	pluginMCP       = "ai-mcp-proxy"
	pluginGuard     = "ai-prompt-guard"
	pluginSanitizer = "ai-sanitizer"
)

// Source is the Kong AI Gateway config-posture source connector.
type Source struct {
	configPath     string
	approvedModels map[string]struct{}
	now            func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Kong AI Gateway config-posture source with default configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "Kong AI Gateway (config posture + policy drift)",
		Description: "Read-only governance of Kong's DECLARED AI config (decK export or Admin API entity JSON): reports an AI proxy scope (ai-proxy / ai-proxy-advanced) with no ai-rate-limiting-advanced (uncapped data path), an ai-mcp-proxy scope with no ai-prompt-guard / ai-sanitizer (ungoverned MCP), a proxied model outside the declared model-access allowlist (gateway-vs-Olivares drift), and a disabled AI plugin (a guard that is off); emits scope→model edges. " +
			"Complements connectors/kong-audit (Admin API audit stream) — this one reads config, never the API. Reads only plugin names, the enabled flag and model/provider labels — never a credential. Offline (no config_path) it is a no-op.",
		ConfigFields: []sdk.ConfigField{
			{Key: "config_path", Type: sdk.FieldString, Description: "File or directory of exported Kong config (*.json / *.yaml / *.yml; decK declarative or Admin API entity JSON). Empty = offline no-op."},
			{Key: "approved_models", Type: sdk.FieldString, Description: "Optional comma-separated allowlist of approved proxied model ids (config.model.name / config.targets[].model.name). A reachable model outside the list is a High drift finding."},
		},
	}
}

// Open reads configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.configPath = strings.TrimSpace(cfg.Get("config_path"))
	s.approvedModels = parseSet(cfg.Get("approved_models"))
	return nil
}

// Gather reads the exported config and emits the posture.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.configPath == "" {
		return nil
	}
	cfgs := s.readConfigs()
	if len(cfgs) == 0 {
		return nil
	}
	st := aggregate(cfgs)
	at := s.clock().UTC()
	for _, o := range s.observations(st, at) {
		if err := sink.Emit(ctx, o); err != nil {
			return err
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

// scopeAgg is the AI posture of one plugin scope (a route, service, consumer, or the
// global scope).
type scopeAgg struct {
	display      string
	hasProxy     bool
	hasRateLimit bool
	hasMCP       bool
	hasGuard     bool
	models       map[string]string // lower(name) -> "provider/name" display
	disabledAI   map[string]struct{}
}

type state struct {
	scopes      map[string]*scopeAgg
	globalRate  bool
	globalGuard bool
	services    int
	routes      int
}

func newState() *state {
	return &state{scopes: map[string]*scopeAgg{}}
}

func (st *state) scope(key, display string) *scopeAgg {
	a := st.scopes[key]
	if a == nil {
		a = &scopeAgg{display: display, models: map[string]string{}, disabledAI: map[string]struct{}{}}
		st.scopes[key] = a
	}
	return a
}

func aggregate(cfgs []kongConfig) *state {
	st := newState()
	for _, cfg := range cfgs {
		st.services += len(cfg.Services)
		for _, svc := range cfg.Services {
			svcKey := "service:" + ident(svc.ID, svc.Name)
			svcDisp := "service " + display(svc.Name, svc.ID)
			for _, p := range svc.Plugins {
				st.add(svcKey, svcDisp, p)
			}
			st.routes += len(svc.Routes)
			for _, rt := range svc.Routes {
				rk := "route:" + ident(rt.ID, rt.Name)
				rd := "route " + display(rt.Name, rt.ID)
				for _, p := range rt.Plugins {
					st.add(rk, rd, p)
				}
			}
		}
		st.routes += len(cfg.Routes)
		for _, rt := range cfg.Routes {
			rk := "route:" + ident(rt.ID, rt.Name)
			rd := "route " + display(rt.Name, rt.ID)
			for _, p := range rt.Plugins {
				st.add(rk, rd, p)
			}
		}
		for _, c := range cfg.Consumers {
			ck := "consumer:" + ident(c.ID, c.Username)
			cd := "consumer " + display(c.Username, c.ID)
			for _, p := range c.Plugins {
				st.add(ck, cd, p)
			}
		}
		for _, p := range cfg.Plugins {
			key, disp := scopeOf(p)
			st.add(key, disp, p)
		}
	}
	return st
}

// add folds one plugin occurrence into its scope.
func (st *state) add(scopeKey, scopeDisplay string, p kongPlugin) {
	name := strings.ToLower(strings.TrimSpace(p.Name))
	if !strings.HasPrefix(name, "ai-") {
		return // only AI plugins bear on this connector's posture
	}
	enabled := p.enabledOrDefault()
	a := st.scope(scopeKey, scopeDisplay)
	if !enabled {
		a.disabledAI[name] = struct{}{}
		return // a disabled plugin is neither an active guard nor an active data path
	}
	switch name {
	case pluginProxy, pluginProxyAdv:
		a.hasProxy = true
		collectModels(a, p.Config)
	case pluginRateLimit:
		a.hasRateLimit = true
		if scopeKey == "global" {
			st.globalRate = true
		}
	case pluginMCP:
		a.hasMCP = true
	case pluginGuard, pluginSanitizer:
		a.hasGuard = true
		if scopeKey == "global" {
			st.globalGuard = true
		}
	}
}

func collectModels(a *scopeAgg, cfg kongPluginConfig) {
	add := func(m *kongModel) {
		if m == nil || strings.TrimSpace(m.Name) == "" {
			return
		}
		disp := m.Name
		if m.Provider != "" {
			disp = m.Provider + "/" + m.Name
		}
		a.models[strings.ToLower(m.Name)] = disp
	}
	add(cfg.Model)
	for _, t := range cfg.Targets {
		add(t.Model)
	}
}

func (s *Source) observations(st *state, at time.Time) []model.Observation {
	var out []model.Observation

	aiScopes := 0
	for _, a := range st.scopes {
		if a.hasProxy || a.hasMCP {
			aiScopes++
		}
	}
	out = append(out, model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectInventory,
		SubjectRef:  "gateway",
		Title: "Kong AI Gateway config posture (services=" + strconv.Itoa(st.services) +
			" routes=" + strconv.Itoa(st.routes) + " ai_scopes=" + strconv.Itoa(aiScopes) + ")",
		DetailHash: redact.Hash("kong-aigw inventory services=" + strconv.Itoa(st.services) + " routes=" + strconv.Itoa(st.routes) + " ai_scopes=" + strconv.Itoa(aiScopes)),
		OccurredAt: at,
	})

	for _, key := range sortedScopeKeys(st.scopes) {
		a := st.scopes[key]
		safe := textscan.SanitizeDisplay(a.display)
		// safeKey is the scope key with its (attacker-controlled) route/service/consumer
		// name sanitized, so a hostile name can never reach a SubjectRef raw.
		safeKey := textscan.SanitizeDisplay(key)

		// Uncapped AI proxy (no rate limiting on this scope or globally).
		if a.hasProxy && !a.hasRateLimit && !st.globalRate {
			out = append(out, model.FindingReport{
				Kind:        "posture",
				Severity:    model.SeverityMedium,
				SubjectKind: subjectRateLimit,
				SubjectRef:  safeKey + "/no-rate-limit",
				Title:       "Kong AI proxy on " + quote(safe) + " has no ai-rate-limiting-advanced (uncapped token/request rate)",
				DetailHash:  redact.Hash("kong-aigw no-rate-limit scope=" + safeKey),
				OccurredAt:  at,
			})
		}
		// Ungoverned MCP (no prompt guard / sanitizer).
		if a.hasMCP && !a.hasGuard && !st.globalGuard {
			out = append(out, model.FindingReport{
				Kind:        "posture",
				Severity:    model.SeverityMedium,
				SubjectKind: subjectMCP,
				SubjectRef:  safeKey + "/ungoverned-mcp",
				Title:       "Kong ai-mcp-proxy on " + quote(safe) + " has no ai-prompt-guard / ai-sanitizer (ungoverned MCP tool traffic)",
				DetailHash:  redact.Hash("kong-aigw ungoverned-mcp scope=" + safeKey),
				OccurredAt:  at,
			})
		}
		// Model-access drift + edges.
		for _, lname := range sortedKeys(a.models) {
			disp := textscan.SanitizeDisplay(a.models[lname])
			out = append(out, model.EdgeObservation{
				OriginKind: "gateway", OriginRef: safe,
				ResourceKind: resourceModel, ResourceRef: disp,
				Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
				ToolRef: "ai-proxy", ObservedAt: at,
			})
			if len(s.approvedModels) > 0 {
				if _, ok := s.approvedModels[lname]; !ok {
					out = append(out, model.FindingReport{
						Kind:        "posture",
						Severity:    model.SeverityHigh,
						SubjectKind: subjectModelPol,
						SubjectRef:  "drift/" + safeKey + "/" + disp,
						Title:       "Kong AI proxy on " + quote(safe) + " reaches model " + quote(disp) + " outside the approved model-access allowlist",
						DetailHash:  redact.Hash("kong-aigw model-drift scope=" + safeKey + " model=" + disp),
						OccurredAt:  at,
					})
				}
			}
		}
		// Disabled AI plugins.
		for _, name := range sortedSet(a.disabledAI) {
			out = append(out, model.FindingReport{
				Kind:        "posture",
				Severity:    model.SeverityLow,
				SubjectKind: subjectPlugin,
				SubjectRef:  safeKey + "/disabled/" + name,
				Title:       "Kong AI plugin " + quote(name) + " on " + quote(safe) + " is disabled (a guard that is present but off)",
				DetailHash:  redact.Hash("kong-aigw disabled-plugin scope=" + safeKey + " plugin=" + name),
				OccurredAt:  at,
			})
		}
	}
	return out
}

// scopeOf resolves a top-level plugin's scope from its refs.
func scopeOf(p kongPlugin) (key, display string) {
	switch {
	case p.Route.set():
		return "route:" + p.Route.ref(), "route " + safeRef(p.Route)
	case p.Service.set():
		return "service:" + p.Service.ref(), "service " + safeRef(p.Service)
	case p.Consumer.set():
		return "consumer:" + p.Consumer.ref(), "consumer " + safeRef(p.Consumer)
	default:
		return "global", "global"
	}
}

func safeRef(r kongRef) string {
	if r.ref() == "" {
		return "(unnamed)"
	}
	return r.ref()
}

func (s *Source) readConfigs() []kongConfig {
	info, err := os.Stat(s.configPath)
	if err != nil {
		return nil
	}
	var files []string
	if info.IsDir() {
		entries, err := os.ReadDir(s.configPath)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			if e.IsDir() || !isConfigFile(e.Name()) {
				continue
			}
			files = append(files, filepath.Join(s.configPath, e.Name()))
			if len(files) >= maxConfigFiles {
				break
			}
		}
	} else {
		files = []string{s.configPath}
	}
	var out []kongConfig
	var totalBytes int64
	for _, p := range files {
		data, ok := readCapped(p, maxConfigBytes)
		if !ok {
			continue
		}
		totalBytes += int64(len(data))
		if cfg, ok := decodeConfig(data); ok {
			out = append(out, cfg)
		}
		if totalBytes > maxTotalBytes {
			break // aggregate ceiling reached; stop before unbounded memory growth
		}
	}
	return out
}

func isConfigFile(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml")
}

func readCapped(path string, limit int64) ([]byte, bool) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied export path, read-only
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 32<<10)
	var total int64
	for {
		n, rerr := f.Read(tmp)
		if n > 0 {
			total += int64(n)
			if total > limit {
				buf = append(buf, tmp[:n-int(total-limit)]...)
				break
			}
			buf = append(buf, tmp[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	return buf, true
}

func ident(id, name string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return id
}

func display(name, id string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	if strings.TrimSpace(id) != "" {
		return id
	}
	return "(unnamed)"
}

func parseSet(csv string) map[string]struct{} {
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

func quote(s string) string { return strconv.Quote(s) }

func sortedScopeKeys(m map[string]*scopeAgg) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
