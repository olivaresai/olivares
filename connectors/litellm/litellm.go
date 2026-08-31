// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package litellm

import (
	"context"
	"math"
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
const Name = "olivares.litellm"

const (
	subjectBudget    = "litellm.budget"
	subjectModelPol  = "litellm.model_policy"
	subjectKey       = "litellm.key"
	subjectInventory = "litellm.gateway"

	resourceKey = "litellm.key"

	maxFiles      = 4096
	maxBytes      = 32 << 20
	maxTotalBytes = 128 << 20 // aggregate ceiling across the whole directory scan

	budgetEpsilon = 0.005 // USD tolerance for a budget-equality compare
)

// Source is the LiteLLM governance source connector.
type Source struct {
	path            string
	approvedModels  map[string]struct{}
	declaredBudgets map[string]float64 // identity (lowercased alias/user/team) -> declared USD cap
	now             func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a LiteLLM source with default configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "LiteLLM (virtual keys + budget drift + identity correlation)",
		Description: "Read-only governance of an exported LiteLLM key/team/budget snapshot: correlates virtual keys with owning identities (edges), and flags an unbounded budget cap, a LiteLLM budget that contradicts the Olivares-declared budget for an identity (drift), a model outside the declared model-access allowlist, an unattributed key, and a retained blocked key. " +
			"Governs LiteLLM as a surface (Olivares is not a gateway). It never emits a CostSample — spend is read only to compare budgets, so LiteLLM-routed cost is not double-counted against the provider connectors. Reads no prompt/completion and never the raw key. Offline (no path) it is a no-op.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Description: "File or directory of exported LiteLLM management JSON (/key/list, /team/list, /user/list — combined object, a bare key array, or JSON-lines). Empty = offline no-op."},
			{Key: "approved_models", Type: sdk.FieldString, Description: "Optional comma-separated allowlist of approved model ids. A key reaching a model outside it (or with an empty all-models list) is a High drift finding."},
			{Key: "declared_budgets", Type: sdk.FieldString, Description: "Optional comma-separated identity=USD pairs (key_alias / user_id / team_id = amount) declaring the Olivares budget. A LiteLLM budget that differs is a High drift finding."},
		},
	}
}

// Open reads configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	s.approvedModels = parseSet(cfg.Get("approved_models"))
	s.declaredBudgets = parseBudgets(cfg.Get("declared_budgets"))
	return nil
}

// Gather reads the exported snapshot and emits the posture.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.path == "" {
		return nil
	}
	exp := s.readExport()
	if len(exp.Keys) == 0 && len(exp.Teams) == 0 && len(exp.Users) == 0 {
		return nil
	}
	at := s.clock().UTC()
	for _, o := range s.observations(exp, at) {
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

func (s *Source) observations(exp litellmExport, at time.Time) []model.Observation {
	var out []model.Observation

	out = append(out, model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectInventory,
		SubjectRef:  "gateway",
		Title: "LiteLLM governance snapshot (keys=" + strconv.Itoa(len(exp.Keys)) +
			" teams=" + strconv.Itoa(len(exp.Teams)) + " users=" + strconv.Itoa(len(exp.Users)) + ")",
		DetailHash: redact.Hash("litellm inventory keys=" + strconv.Itoa(len(exp.Keys)) + " teams=" + strconv.Itoa(len(exp.Teams)) + " users=" + strconv.Itoa(len(exp.Users))),
		OccurredAt: at,
	})

	for _, k := range sortedKeysByID(exp.Keys) {
		id := keyID(k)
		safe := textscan.SanitizeDisplay(id)
		owner := keyOwner(k)

		// Identity correlation edge (owner -> virtual key).
		originRef := textscan.SanitizeDisplay(owner)
		if owner == "" {
			originRef = "(unattributed)"
		}
		out = append(out, model.EdgeObservation{
			OriginKind: "identity", OriginRef: originRef,
			ResourceKind: resourceKey, ResourceRef: safe,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: confidenceFor(owner),
			ToolRef: "litellm", ObservedAt: at,
		})

		// Unattributed key.
		if owner == "" && strings.TrimSpace(k.KeyAlias) == "" {
			out = append(out, model.FindingReport{
				Kind:        "posture",
				Severity:    model.SeverityMedium,
				SubjectKind: subjectKey,
				SubjectRef:  safe + "/unattributed",
				Title:       "LiteLLM virtual key " + quote(safe) + " has no owner (no user_id, team_id, or alias)",
				DetailHash:  redact.Hash("litellm unattributed key=" + safe),
				OccurredAt:  at,
			})
		}
		// Blocked-but-retained.
		if k.Blocked {
			out = append(out, model.FindingReport{
				Kind:        "posture",
				Severity:    model.SeverityInfo,
				SubjectKind: subjectKey,
				SubjectRef:  safe + "/blocked",
				Title:       "LiteLLM virtual key " + quote(safe) + " is blocked but still present in the export",
				DetailHash:  redact.Hash("litellm blocked key=" + safe),
				OccurredAt:  at,
			})
		}
		// Budget posture (no cap / drift vs declared).
		out = append(out, s.budgetObservations("key", id, safe, k.MaxBudget, at, k.KeyAlias, k.UserID, k.TeamID)...)
		// Model-access drift.
		out = append(out, s.modelObservations("key", id, safe, k.Models, at)...)
	}

	for _, tm := range sortedTeams(exp.Teams) {
		id := firstNonEmpty(tm.TeamAlias, tm.TeamID)
		if id == "" {
			continue
		}
		safe := textscan.SanitizeDisplay(id)
		out = append(out, s.budgetObservations("team", id, safe, tm.MaxBudget, at, tm.TeamAlias, tm.TeamID)...)
		out = append(out, s.modelObservations("team", id, safe, tm.Models, at)...)
	}

	for _, u := range sortedUsers(exp.Users) {
		if strings.TrimSpace(u.UserID) == "" {
			continue
		}
		safe := textscan.SanitizeDisplay(u.UserID)
		out = append(out, s.budgetObservations("user", u.UserID, safe, u.MaxBudget, at, u.UserID)...)
		out = append(out, s.modelObservations("user", u.UserID, safe, u.Models, at)...)
	}
	return out
}

// budgetObservations flags an absent budget cap and a drift against the declared
// budget (matched against any of the identity's names).
func (s *Source) budgetObservations(kind, id, safe string, maxBudget *float64, at time.Time, names ...string) []model.Observation {
	var out []model.Observation
	names = append(names, id)
	if maxBudget == nil {
		out = append(out, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectBudget,
			SubjectRef:  kind + "/" + safe + "/no-budget",
			Title:       "LiteLLM " + kind + " " + quote(safe) + " has no max_budget (unbounded spend cap)",
			DetailHash:  redact.Hash("litellm no-budget " + kind + "=" + safe),
			OccurredAt:  at,
		})
	}
	if declared, ok := s.lookupBudget(names...); ok {
		if maxBudget == nil || math.Abs(*maxBudget-declared) > budgetEpsilon {
			have := "unset"
			if maxBudget != nil {
				have = strconv.FormatFloat(*maxBudget, 'f', -1, 64)
			}
			out = append(out, model.FindingReport{
				Kind:        "posture",
				Severity:    model.SeverityHigh,
				SubjectKind: subjectBudget,
				SubjectRef:  "drift/" + kind + "/" + safe,
				Title:       "LiteLLM " + kind + " " + quote(safe) + " budget (" + have + ") contradicts the Olivares-declared budget (" + strconv.FormatFloat(declared, 'f', -1, 64) + ")",
				DetailHash:  redact.Hash("litellm budget-drift " + kind + "=" + safe + " have=" + have + " declared=" + strconv.FormatFloat(declared, 'f', -1, 64)),
				OccurredAt:  at,
			})
		}
	}
	return out
}

// modelObservations flags all-models access and any model outside the allowlist. kind
// (key/team/user) namespaces the SubjectRef so identities in different namespaces that
// share a display name never collide.
func (s *Source) modelObservations(kind, id, safe string, models []string, at time.Time) []model.Observation {
	if len(s.approvedModels) == 0 {
		return nil
	}
	var out []model.Observation
	if len(models) == 0 {
		out = append(out, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityHigh,
			SubjectKind: subjectModelPol,
			SubjectRef:  kind + "/" + safe + "/all-models",
			Title:       "LiteLLM " + kind + " " + quote(safe) + " has an empty models list (unrestricted all-models access) under a model-access policy",
			DetailHash:  redact.Hash("litellm all-models " + kind + "=" + safe),
			OccurredAt:  at,
		})
		return out
	}
	for _, m := range models {
		mm := strings.ToLower(strings.TrimSpace(m))
		if mm == "" || mm == "all-team-models" || mm == "all-proxy-models" {
			continue
		}
		if _, ok := s.approvedModels[mm]; !ok {
			safeModel := textscan.SanitizeDisplay(m)
			out = append(out, model.FindingReport{
				Kind:        "posture",
				Severity:    model.SeverityHigh,
				SubjectKind: subjectModelPol,
				SubjectRef:  "drift/" + kind + "/" + safe + "/" + safeModel,
				Title:       "LiteLLM " + kind + " " + quote(safe) + " may reach model " + quote(safeModel) + " outside the approved model-access allowlist",
				DetailHash:  redact.Hash("litellm model-drift " + kind + "=" + safe + " model=" + safeModel),
				OccurredAt:  at,
			})
		}
	}
	return out
}

func (s *Source) lookupBudget(names ...string) (float64, bool) {
	for _, n := range names {
		if v, ok := s.declaredBudgets[strings.ToLower(strings.TrimSpace(n))]; ok {
			return v, true
		}
	}
	return 0, false
}

func (s *Source) readExport() litellmExport {
	info, err := os.Stat(s.path)
	if err != nil {
		return litellmExport{}
	}
	var files []string
	if info.IsDir() {
		entries, err := os.ReadDir(s.path)
		if err != nil {
			return litellmExport{}
		}
		for _, e := range entries {
			if e.IsDir() || !isExportFile(e.Name()) {
				continue
			}
			files = append(files, filepath.Join(s.path, e.Name()))
			if len(files) >= maxFiles {
				break
			}
		}
	} else {
		files = []string{s.path}
	}
	var merged litellmExport
	var totalBytes int64
	for _, p := range files {
		data, ok := readCapped(p, maxBytes)
		if !ok {
			continue
		}
		totalBytes += int64(len(data))
		e := decodeExport(data)
		merged.Keys = append(merged.Keys, e.Keys...)
		merged.Teams = append(merged.Teams, e.Teams...)
		merged.Users = append(merged.Users, e.Users...)
		if totalBytes > maxTotalBytes {
			break // aggregate ceiling reached; stop before unbounded memory growth
		}
	}
	return merged
}

func isExportFile(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".ndjson")
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

func keyID(k litellmKey) string {
	if strings.TrimSpace(k.KeyAlias) != "" {
		return k.KeyAlias
	}
	if strings.TrimSpace(k.Token) != "" {
		return "tok:" + redact.Hash(k.Token)[:12]
	}
	return "(anon)"
}

func keyOwner(k litellmKey) string {
	return firstNonEmpty(k.UserID, k.TeamID)
}

func confidenceFor(owner string) model.Confidence {
	if strings.TrimSpace(owner) != "" {
		return model.ConfidenceAttributed
	}
	return model.ConfidenceApproximate
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

func parseBudgets(csv string) map[string]float64 {
	out := map[string]float64{}
	for _, pair := range strings.Split(csv, ",") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(k))
		amt, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if name == "" || err != nil || math.IsNaN(amt) || math.IsInf(amt, 0) || amt < 0 {
			continue
		}
		out[name] = amt
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

func sortedKeysByID(in []litellmKey) []litellmKey {
	out := append([]litellmKey(nil), in...)
	sort.Slice(out, func(i, j int) bool { return keyID(out[i]) < keyID(out[j]) })
	return out
}

func sortedTeams(in []litellmTeam) []litellmTeam {
	out := append([]litellmTeam(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		return firstNonEmpty(out[i].TeamAlias, out[i].TeamID) < firstNonEmpty(out[j].TeamAlias, out[j].TeamID)
	})
	return out
}

func sortedUsers(in []litellmUser) []litellmUser {
	out := append([]litellmUser(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out
}
