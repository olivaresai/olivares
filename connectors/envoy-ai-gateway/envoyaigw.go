// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package envoyaigw

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
const Name = "olivares.envoy-ai-gateway"

const (
	subjectBackend   = "envoy_aigw.backend"
	subjectMCP       = "envoy_aigw.mcp"
	subjectModelPol  = "envoy_aigw.model_policy"
	subjectFinOps    = "envoy_aigw.finops"
	subjectInventory = "envoy_aigw.gateway"

	resourceBackend = "envoy_aigw.backend"

	// maxConfigFiles / maxConfigBytes bound one file; maxTotalBytes bounds the whole
	// directory scan, so a large or hostile snapshot cannot drag discovery into
	// unbounded memory (file-count × per-file cap would otherwise reach tens of GiB).
	maxConfigFiles = 4096
	maxConfigBytes = 16 << 20
	maxTotalBytes  = 128 << 20
)

// Kinds this connector reads (compared case-insensitively).
const (
	kindBackend  = "aiservicebackend"
	kindBSP      = "backendsecuritypolicy"
	kindRoute    = "aigatewayroute"
	kindMCPRoute = "mcproute"
	kindQuota    = "quotapolicy"
)

// Source is the Envoy AI Gateway config-posture source connector.
type Source struct {
	configPath     string
	approvedModels map[string]struct{}
	now            func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns an Envoy AI Gateway config-posture source with default configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "Envoy AI Gateway (config posture + policy drift)",
		Description: "Read-only governance of the Envoy AI Gateway's DECLARED config (applied v1alpha1 CRDs, exported by the operator): reports a reachable AIServiceBackend with no BackendSecurityPolicy (unauthenticated upstream), an MCPRoute with no securityPolicy or no toolSelector allowlist, a served model outside the declared model-access allowlist (gateway-vs-Olivares drift), and a route with neither llmRequestCosts nor a QuotaPolicy (FinOps/quota blind spot); emits route→backend edges. " +
			"Complements connectors/ai-gateway (usage/cost) and connectors/envoy (L7 mesh) — this one reads config, never traffic. Reads only names/kinds/schema labels/policy TYPE and boolean presence — never a prompt, completion, or secret. Offline (no config_path) it is a no-op.",
		ConfigFields: []sdk.ConfigField{
			{Key: "config_path", Type: sdk.FieldString, Description: "File or directory of exported Envoy AI Gateway CRD manifests (*.json / *.yaml / *.yml; a single object, a List, or a stream). Empty = offline no-op."},
			{Key: "approved_models", Type: sdk.FieldString, Description: "Optional comma-separated allowlist of approved served model ids (backendRef.modelNameOverride). A reachable model outside the list is a High drift finding."},
		},
	}
}

// Open reads configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.configPath = strings.TrimSpace(cfg.Get("config_path"))
	s.approvedModels = parseSet(cfg.Get("approved_models"))
	return nil
}

// Gather reads the exported config and emits the posture. Offline (no config_path)
// it emits nothing.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.configPath == "" {
		return nil
	}
	objs := s.readObjects()
	if len(objs) == 0 {
		return nil
	}
	f := aggregate(objs)
	at := s.clock().UTC()
	for _, o := range s.observations(f, at) {
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

// facts is the aggregated config view.
type facts struct {
	backends        []nsName          // AIServiceBackend ns/name
	backendSchema   map[string]string // ns/name -> schema.Name
	securedBackends map[string]struct{}
	quotaTargets    map[string]struct{} // ns/name of routes/backends a QuotaPolicy targets
	routes          []routeFact
	mcpRoutes       []mcpFact
}

type nsName struct{ ns, name string }

func (n nsName) key() string { return n.ns + "/" + n.name }

type routeFact struct {
	ns, name    string
	backendRefs []aiRouteBackendRef
	hasCost     bool
}

type mcpFact struct {
	ns, name     string
	routeSecured bool
	backends     []mcpBackendRef
}

func aggregate(objs []k8sObject) *facts {
	f := &facts{
		backendSchema:   map[string]string{},
		securedBackends: map[string]struct{}{},
		quotaTargets:    map[string]struct{}{},
	}
	for _, o := range objs {
		ns := o.Metadata.Namespace
		switch strings.ToLower(o.Kind) {
		case kindBackend:
			bn := nsName{ns, o.Metadata.Name}
			f.backends = append(f.backends, bn)
			if o.Spec.Schema != nil {
				f.backendSchema[bn.key()] = o.Spec.Schema.Name
			}
		case kindBSP:
			for _, t := range o.Spec.TargetRefs {
				tns := firstNonEmpty(t.Namespace, ns)
				f.securedBackends[nsName{tns, t.Name}.key()] = struct{}{}
			}
		case kindQuota:
			for _, t := range o.Spec.TargetRefs {
				tns := firstNonEmpty(t.Namespace, ns)
				f.quotaTargets[nsName{tns, t.Name}.key()] = struct{}{}
			}
		case kindRoute:
			rf := routeFact{ns: ns, name: o.Metadata.Name}
			for _, r := range o.Spec.Rules {
				rf.backendRefs = append(rf.backendRefs, r.BackendRefs...)
				if len(r.LLMRequestCosts) > 0 {
					rf.hasCost = true
				}
			}
			f.routes = append(f.routes, rf)
		case kindMCPRoute:
			f.mcpRoutes = append(f.mcpRoutes, mcpFact{
				ns: ns, name: o.Metadata.Name,
				routeSecured: present(o.Spec.SecurityPolicy),
				backends:     o.Spec.BackendRefs,
			})
		}
	}
	return f
}

func (s *Source) observations(f *facts, at time.Time) []model.Observation {
	var out []model.Observation

	// Inventory.
	out = append(out, model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectInventory,
		SubjectRef:  "gateway",
		Title: "Envoy AI Gateway config posture (backends=" + strconv.Itoa(len(f.backends)) +
			" secured=" + strconv.Itoa(len(f.securedBackends)) + " routes=" + strconv.Itoa(len(f.routes)) +
			" mcp_routes=" + strconv.Itoa(len(f.mcpRoutes)) + ")",
		DetailHash: redact.Hash("envoy-aigw inventory backends=" + strconv.Itoa(len(f.backends)) + " routes=" + strconv.Itoa(len(f.routes))),
		OccurredAt: at,
	})

	// 1) Unauthenticated backends (no BackendSecurityPolicy).
	for _, b := range sortedNsNames(f.backends) {
		if _, ok := f.securedBackends[b.key()]; ok {
			continue
		}
		safe := textscan.SanitizeDisplay(b.name)
		schema := textscan.SanitizeDisplay(f.backendSchema[b.key()])
		out = append(out, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityHigh,
			SubjectKind: subjectBackend,
			SubjectRef:  safe,
			Title:       "Envoy AI Gateway backend " + quote(safe) + " is reachable with no BackendSecurityPolicy (unauthenticated upstream)",
			DetailHash:  redact.Hash("envoy-aigw unauth-backend ns=" + b.ns + " name=" + safe + " schema=" + schema),
			OccurredAt:  at,
		})
	}

	// 2) MCP passthrough posture.
	for _, m := range sortedMCP(f.mcpRoutes) {
		safe := textscan.SanitizeDisplay(m.name)
		anyBackendSecured := false
		unrestrictedTools := false
		for _, b := range m.backends {
			if present(b.SecurityPolicy) {
				anyBackendSecured = true
			}
			if !present(b.ToolSelector) {
				unrestrictedTools = true
			}
		}
		if !m.routeSecured && !anyBackendSecured {
			out = append(out, model.FindingReport{
				Kind:        "posture",
				Severity:    model.SeverityHigh,
				SubjectKind: subjectMCP,
				SubjectRef:  safe + "/no-security-policy",
				Title:       "Envoy AI Gateway MCPRoute " + quote(safe) + " has no securityPolicy (unauthenticated MCP passthrough)",
				DetailHash:  redact.Hash("envoy-aigw mcp-unauth ns=" + m.ns + " name=" + safe),
				OccurredAt:  at,
			})
		}
		if unrestrictedTools && len(m.backends) > 0 {
			out = append(out, model.FindingReport{
				Kind:        "posture",
				Severity:    model.SeverityMedium,
				SubjectKind: subjectMCP,
				SubjectRef:  safe + "/no-tool-selector",
				Title:       "Envoy AI Gateway MCPRoute " + quote(safe) + " exposes every tool on a backend (no toolSelector allowlist)",
				DetailHash:  redact.Hash("envoy-aigw mcp-alltools ns=" + m.ns + " name=" + safe),
				OccurredAt:  at,
			})
		}
	}

	// 3) Model-access drift + 4) FinOps blind spot + edges, per route.
	for _, r := range sortedRoutes(f.routes) {
		safeRoute := textscan.SanitizeDisplay(r.name)
		routeKey := nsName{r.ns, r.name}.key()
		hasQuota := false
		if _, ok := f.quotaTargets[routeKey]; ok {
			hasQuota = true
		}
		for _, br := range r.backendRefs {
			// edge route -> backend
			out = append(out, model.EdgeObservation{
				OriginKind: "gateway", OriginRef: safeRoute,
				ResourceKind: resourceBackend, ResourceRef: textscan.SanitizeDisplay(br.Name),
				Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
				ToolRef: "route", ObservedAt: at,
			})
			// backend under a QuotaPolicy also counts as quota coverage for the route.
			if _, ok := f.quotaTargets[nsName{firstNonEmpty(br.Namespace, r.ns), br.Name}.key()]; ok {
				hasQuota = true
			}
			// model drift
			if len(s.approvedModels) > 0 && br.ModelNameOverride != "" {
				if _, ok := s.approvedModels[strings.ToLower(br.ModelNameOverride)]; !ok {
					safeModel := textscan.SanitizeDisplay(br.ModelNameOverride)
					out = append(out, model.FindingReport{
						Kind:        "posture",
						Severity:    model.SeverityHigh,
						SubjectKind: subjectModelPol,
						SubjectRef:  "drift/" + safeModel,
						Title:       "Envoy AI Gateway route " + quote(safeRoute) + " serves model " + quote(safeModel) + " outside the approved model-access allowlist",
						DetailHash:  redact.Hash("envoy-aigw model-drift route=" + safeRoute + " model=" + safeModel),
						OccurredAt:  at,
					})
				}
			}
		}
		if !r.hasCost && !hasQuota {
			out = append(out, model.FindingReport{
				Kind:        "posture",
				Severity:    model.SeverityLow,
				SubjectKind: subjectFinOps,
				SubjectRef:  safeRoute + "/no-cost-or-quota",
				Title:       "Envoy AI Gateway route " + quote(safeRoute) + " has neither llmRequestCosts nor a QuotaPolicy (cost unmetered, spend uncapped)",
				DetailHash:  redact.Hash("envoy-aigw finops-blind route=" + safeRoute),
				OccurredAt:  at,
			})
		}
	}
	return out
}

// readObjects scans the config path (bounded) and decodes every manifest file.
func (s *Source) readObjects() []k8sObject {
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
	var out []k8sObject
	var totalBytes int64
	for _, p := range files {
		data, ok := readCapped(p, maxConfigBytes)
		if !ok {
			continue
		}
		totalBytes += int64(len(data))
		out = append(out, decodeObjects(data)...)
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func quote(s string) string { return strconv.Quote(s) }

func sortedNsNames(in []nsName) []nsName {
	out := append([]nsName(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

func sortedRoutes(in []routeFact) []routeFact {
	out := append([]routeFact(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].ns+"/"+out[i].name < out[j].ns+"/"+out[j].name })
	return out
}

func sortedMCP(in []mcpFact) []mcpFact {
	out := append([]mcpFact(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].ns+"/"+out[i].name < out[j].ns+"/"+out[j].name })
	return out
}
