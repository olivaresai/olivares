// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package paperclip

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.paperclip"

const (
	// CostType tags the cost samples so FinOps can tell Paperclip-reported spend
	// from provider-billed spend.
	CostType = "paperclip"

	metricRuns = "paperclip.heartbeat_runs"

	defaultLookbackDays = 3
	maxLookbackDays     = 31
	defaultTimeout      = 30 * time.Second
	// defaultMaxRuns is Paperclip's own ceiling for one heartbeat-runs listing.
	defaultMaxRuns = 1000
	// maxCompanies bounds the per-company request fan-out (2 + lookback_days each).
	maxCompanies = 500
	centsToMicro = 10_000
	// Plausibility ceilings for remote numbers: 1e12 cents is $10 billion, 1e15
	// tokens is far beyond any real day. A larger value is a broken or hostile answer.
	maxCents  = 1e12
	maxTokens = 1e15
	// maxTextLen bounds any remote free text that reaches a roster row or finding.
	maxTextLen = 128

	day = 24 * time.Hour
)

var (
	// idPattern is the shape of a Paperclip id (a UUID). It also keeps "." and ".."
	// out of the URL paths built from remote ids.
	idPattern = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)
	// tokenPattern is the shape of a Paperclip enum value (status, adapter type).
	tokenPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	// inFlight run statuses are not counted: such a run would be bucketed under a
	// status it later leaves, and the ledger keeps the stale bucket.
	inFlight = map[string]bool{"queued": true, "scheduled_retry": true, "running": true}

	errShape = errors.New("paperclip: unexpected response shape")
)

// Source is the Paperclip observe-only source connector and roster provider.
type Source struct {
	baseURL      string
	apiKey       string
	lookbackDays int
	timeout      time.Duration
	maxRuns      int

	doer httpx.Doer       // injected transport (tests); nil => http.Client{Timeout}
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns a Paperclip connector with default configuration (offline until a
// base_url is set).
func New() *Source {
	return &Source{lookbackDays: defaultLookbackDays, timeout: defaultTimeout, maxRuns: defaultMaxRuns}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "Paperclip (observe-only)",
		Description: "Reads a Paperclip orchestrator over its REST API with a board API key (GET only, never a write): " +
			"each company becomes a group collection and each agent an NHI roster row (kind paperclip/<adapterType>, with role, title, reports_to), " +
			"finished heartbeat runs of completed days become metric samples, and Paperclip-reported spend becomes advisory cost samples (cost_type=paperclip). " +
			"HONEST LIMITS: advisory only (Olivares sees what Paperclip reports, it neither starts nor confines the agents), and cost is aggregated per day because Paperclip exposes no raw cost-event list. Empty base_url = offline no-op.",
		ConfigFields: []sdk.ConfigField{
			{Key: "base_url", Type: sdk.FieldString, Description: "Paperclip server root, for example http://127.0.0.1:3100 (the connector adds /api). Empty = offline no-op."},
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "Paperclip board API key reference (read-only use, never persisted or logged). Empty sends no credential, which only a local_trusted Paperclip accepts."},
			{Key: "lookback_days", Type: sdk.FieldInt, Default: strconv.Itoa(defaultLookbackDays), Description: "Completed UTC days of runs and cost re-read on each pass (1-31). Re-reads are idempotent: each day is one bucket."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "Per-request HTTP timeout."},
		},
	}
}

// Open reads configuration. It never contacts the network.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	base := strings.TrimRight(strings.TrimSpace(cfg.Get("base_url")), "/")
	if base != "" {
		// The message never repeats the value: a URL can carry userinfo.
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("paperclip: base_url must be an http(s) URL without credentials, query or fragment")
		}
	}
	s.baseURL = base
	s.apiKey = strings.TrimSpace(cfg.Get("api_key"))
	s.lookbackDays = cfg.GetInt("lookback_days", s.lookbackDays)
	if s.lookbackDays < 1 || s.lookbackDays > maxLookbackDays {
		return fmt.Errorf("paperclip: lookback_days must be between 1 and %d", maxLookbackDays)
	}
	s.timeout = cfg.GetDuration("timeout", s.timeout)
	if s.timeout <= 0 {
		s.timeout = defaultTimeout
	}
	return nil
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

func (s *Source) offline() bool { return s.baseURL == "" }

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Source) client() *httpx.Client {
	doer := s.doer
	if doer == nil {
		doer = &http.Client{Timeout: s.timeout}
	}
	return httpx.New(s.baseURL, doer, httpx.Bearer(s.apiKey), nil)
}

// --- Paperclip wire shapes: only the fields the mapping needs. adapterConfig and
// runtimeConfig (which can hold env values) are deliberately never decoded. ---

type company struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type agent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	ReportsTo   string `json:"reportsTo"`
	AdapterType string `json:"adapterType"`
}

type run struct {
	AgentID   string    `json:"agentId"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
}

// costRow uses pointers so a renamed or missing number is an error, never a zero.
type costRow struct {
	AgentID      string   `json:"agentId"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	CostCents    *float64 `json:"costCents"`
	InputTokens  *float64 `json:"inputTokens"`
	OutputTokens *float64 `json:"outputTokens"`
}

func companyRef(id string) string { return "paperclip/company/" + id }
func agentRef(id string) string   { return "paperclip/agent/" + id }

// clean makes remote free text safe for a roster row or a finding title.
func clean(s string) string {
	// SanitizeDisplay strips invisible and control runes and scrubs secret shapes but
	// keeps newlines and tabs; fold them so a name is always one line.
	s = strings.Join(strings.Fields(textscan.SanitizeDisplay(s)), " ")
	if r := []rune(s); len(r) > maxTextLen {
		s = string(r[:maxTextLen])
	}
	return s
}

// token normalizes a remote enum value; anything off-shape becomes "unknown".
func token(s string) string {
	if s = strings.ToLower(strings.TrimSpace(s)); tokenPattern.MatchString(s) {
		return s
	}
	return "unknown"
}

func shapeErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errShape, fmt.Sprintf(format, args...))
}

// amount validates a remote number: present, and within [0, limit].
func amount(v *float64, limit float64, what string) (float64, error) {
	if v == nil {
		return 0, shapeErr("%s missing", what)
	}
	if *v < 0 || *v > limit {
		return 0, shapeErr("%s out of range", what)
	}
	return *v, nil
}

// companyView is one company with its agents. agentsErr records a tolerated
// (403/404) agents read so Gather can report it instead of dropping the company.
type companyView struct {
	company   company
	agents    []agent
	agentsErr error
}

// tolerated reports whether err is Paperclip honestly refusing one leg (no right
// or no such route) rather than a transport or server fault.
func tolerated(err error) bool {
	var se *httpx.StatusError
	return errors.As(err, &se) && (se.Status == http.StatusForbidden || se.Status == http.StatusNotFound)
}

// loadEstate reads companies and their agents, ordered by id, first duplicate wins.
// An id outside the Paperclip shape fails the read: it would be dropped silently
// or interpolated into a URL path.
func (s *Source) loadEstate(ctx context.Context, c *httpx.Client, tolerate bool) ([]companyView, error) {
	var companies []company
	if err := c.GetJSON(ctx, "/api/companies", nil, &companies); err != nil {
		return nil, fmt.Errorf("paperclip: list companies: %w", err)
	}
	if len(companies) > maxCompanies {
		return nil, shapeErr("more than %d companies", maxCompanies)
	}
	for _, co := range companies {
		if !idPattern.MatchString(co.ID) {
			return nil, shapeErr("company id")
		}
	}
	sort.SliceStable(companies, func(i, j int) bool { return companies[i].ID < companies[j].ID })
	seenCompany, seenAgent := map[string]bool{}, map[string]bool{}
	out := make([]companyView, 0, len(companies))
	for _, co := range companies {
		if seenCompany[co.ID] {
			continue
		}
		seenCompany[co.ID] = true
		v := companyView{company: co}
		var agents []agent
		err := c.GetJSON(ctx, "/api/companies/"+url.PathEscape(co.ID)+"/agents", nil, &agents)
		switch {
		case err == nil:
		case tolerate && tolerated(err):
			v.agentsErr = err
		default:
			return nil, fmt.Errorf("paperclip: list agents: %w", err)
		}
		for _, a := range agents {
			if !idPattern.MatchString(a.ID) || (a.ReportsTo != "" && !idPattern.MatchString(a.ReportsTo)) {
				return nil, shapeErr("agent id")
			}
		}
		sort.SliceStable(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
		for _, a := range agents {
			if !seenAgent[a.ID] {
				seenAgent[a.ID] = true
				v.agents = append(v.agents, a)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// Snapshot maps companies to group collections and agents to NHI roster rows.
// A partial roster is never returned: any failed leg fails the snapshot.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourcePaperclip, CapturedAt: s.clock().UTC()}
	if s.offline() {
		return g, nil
	}
	estate, err := s.loadEstate(ctx, s.client(), false)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, v := range estate {
		cref := companyRef(v.company.ID)
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref: cref, Kind: identitysource.KindGroup, DisplayName: clean(v.company.Name), Source: identitysource.SourcePaperclip,
			Attributes: map[string]string{"status": token(v.company.Status)},
		})
		for _, a := range v.agents {
			ref := agentRef(a.ID)
			status := token(a.Status)
			g.Identities = append(g.Identities, identitysource.Identity{
				Ref:         ref,
				Type:        identitysource.PrincipalNHI,
				Kind:        "paperclip/" + token(a.AdapterType),
				DisplayName: clean(a.Name),
				Source:      identitysource.SourcePaperclip,
				Disabled:    status == "paused" || status == "terminated",
				Attributes:  agentAttributes(a, cref, status),
			})
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef: ref, MemberKind: identitysource.MemberIdentity, CollectionRef: cref, Source: identitysource.SourcePaperclip,
			})
		}
	}
	return g, nil
}

func agentAttributes(a agent, cref, status string) map[string]string {
	attrs := map[string]string{"company": cref, "status": status}
	for k, v := range map[string]string{"role": a.Role, "title": a.Title} {
		if v = clean(v); v != "" {
			attrs[k] = v
		}
	}
	if a.ReportsTo != "" {
		attrs["reports_to"] = agentRef(a.ReportsTo)
	}
	return attrs
}

// Gather emits the inventory summary, finished runs and advisory cost.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.offline() {
		return nil
	}
	c := s.client()
	estate, err := s.loadEstate(ctx, c, true)
	if err != nil {
		return err
	}
	at := s.clock().UTC()
	agents, unreadable := 0, 0
	for _, v := range estate {
		agents += len(v.agents)
		if v.agentsErr != nil {
			unreadable++
		}
	}
	title := "Paperclip snapshot (companies=" + strconv.Itoa(len(estate)) + " agents=" + strconv.Itoa(agents)
	if unreadable > 0 {
		title += " companies-with-unreadable-agents=" + strconv.Itoa(unreadable)
	}
	title += ")"
	if err := sink.Emit(ctx, model.FindingReport{
		Kind: "inventory", Severity: model.SeverityInfo, SubjectKind: "paperclip.instance", SubjectRef: "paperclip",
		Title: title, DetailHash: redact.Hash(title), OccurredAt: at,
	}); err != nil {
		return err
	}
	for _, v := range estate {
		if v.agentsErr != nil {
			if err := s.coverage(ctx, sink, v.company, "agents", v.agentsErr, at); err != nil {
				return err
			}
		}
		if err := s.gatherRuns(ctx, c, sink, v.company, at); err != nil {
			return err
		}
		if err := s.gatherCosts(ctx, c, sink, v.company, at); err != nil {
			return err
		}
	}
	return nil
}

// coverage reports a leg Paperclip refused, so a gap is visible and never read as
// "no activity".
func (s *Source) coverage(ctx context.Context, sink sdk.Sink, co company, leg string, cause error, at time.Time) error {
	var se *httpx.StatusError
	status := "error"
	if errors.As(cause, &se) {
		status = strconv.Itoa(se.Status)
	}
	title := "Paperclip " + leg + " not readable for company " + strconv.Quote(clean(co.Name)) + " (status " + status + ")"
	return s.emitCoverage(ctx, sink, co, leg, title, at)
}

func (s *Source) emitCoverage(ctx context.Context, sink sdk.Sink, co company, leg, title string, at time.Time) error {
	return sink.Emit(ctx, model.FindingReport{
		Kind: "coverage", Severity: model.SeverityInfo, SubjectKind: "paperclip.company", SubjectRef: companyRef(co.ID),
		Title: title, DetailHash: redact.Hash("paperclip coverage|" + leg + "|" + co.ID + "|" + title), OccurredAt: at,
	})
}

// window returns the completed UTC days the pass re-reads, oldest first. Today is
// still open, so it is never read: a half-day bucket would change on every pass.
func (s *Source) window(at time.Time) []time.Time {
	today := at.Truncate(day)
	days := make([]time.Time, 0, s.lookbackDays)
	for i := s.lookbackDays; i >= 1; i-- {
		days = append(days, today.AddDate(0, 0, -i))
	}
	return days
}

func (s *Source) gatherRuns(ctx context.Context, c *httpx.Client, sink sdk.Sink, co company, at time.Time) error {
	var runs []run
	q := url.Values{"summary": {"true"}, "limit": {strconv.Itoa(s.maxRuns)}}
	if err := c.GetJSON(ctx, "/api/companies/"+url.PathEscape(co.ID)+"/heartbeat-runs", q, &runs); err != nil {
		if tolerated(err) {
			return s.coverage(ctx, sink, co, "runs", err, at)
		}
		return fmt.Errorf("paperclip: list runs: %w", err)
	}
	days := s.window(at)
	cutoff, end := days[0], days[len(days)-1].Add(day)
	type bucket struct {
		agent, status string
		day           time.Time
	}
	counts := map[bucket]int64{}
	var oldest time.Time
	for _, r := range runs {
		status := token(r.Status)
		if !idPattern.MatchString(r.AgentID) {
			return shapeErr("run agent id")
		}
		if r.StartedAt.IsZero() {
			if !inFlight[status] {
				return shapeErr("finished run without startedAt")
			}
			continue
		}
		started := r.StartedAt.UTC()
		if oldest.IsZero() || started.Before(oldest) {
			oldest = started
		}
		if inFlight[status] || !started.Before(end) {
			continue
		}
		counts[bucket{agent: r.AgentID, status: status, day: started.Truncate(day)}]++
	}
	if len(runs) >= s.maxRuns {
		title := "Paperclip runs for company " + strconv.Quote(clean(co.Name)) + " may be truncated (limit " + strconv.Itoa(s.maxRuns) + " reached)"
		if err := s.emitCoverage(ctx, sink, co, "runs-truncated", title, at); err != nil {
			return err
		}
		// The oldest day the listing reaches may be cut off mid-day: do not report it.
		if d := oldest.Truncate(day).Add(day); !oldest.IsZero() && d.After(cutoff) {
			cutoff = d
		}
	}
	keys := make([]bucket, 0, len(counts))
	for k := range counts {
		if !k.day.Before(cutoff) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if !a.day.Equal(b.day) {
			return a.day.Before(b.day)
		}
		if a.agent != b.agent {
			return a.agent < b.agent
		}
		return a.status < b.status
	})
	for _, k := range keys {
		if err := sink.Emit(ctx, model.MetricSample{
			Name: metricRuns, Value: counts[k], Unit: "runs", SubjectKind: "agent", SubjectRef: agentRef(k.agent),
			OccurredAt: k.day, Dimensions: map[string]string{"status": k.status},
		}); err != nil {
			return err
		}
	}
	return nil
}

// dayCost is the spend of one agent on one model for one day, summed across the
// biller and billing-type rows Paperclip splits it into (they share one ledger key).
type dayCost struct {
	agent, provider, model string
	cents, input, output   float64
}

// ponytail: one by-agent-model read per completed day per company, because Paperclip
// exposes no raw cost-event list; upgrade trigger: a cost-events read route.
func (s *Source) gatherCosts(ctx context.Context, c *httpx.Client, sink sdk.Sink, co company, at time.Time) error {
	const stamp = "2006-01-02T15:04:05.000Z"
	for _, d := range s.window(at) {
		q := url.Values{"from": {d.Format(stamp)}, "to": {d.Add(day - time.Millisecond).Format(stamp)}}
		var rows []costRow
		if err := c.GetJSON(ctx, "/api/companies/"+url.PathEscape(co.ID)+"/costs/by-agent-model", q, &rows); err != nil {
			if tolerated(err) {
				return s.coverage(ctx, sink, co, "costs", err, at)
			}
			return fmt.Errorf("paperclip: read costs: %w", err)
		}
		merged, err := mergeCosts(rows)
		if err != nil {
			return err
		}
		for _, m := range merged {
			if err := sink.Emit(ctx, model.CostSample{
				ProviderRef:  clean(m.provider),
				ModelRef:     clean(m.model),
				InputTokens:  int64(math.Round(m.input)),
				OutputTokens: int64(math.Round(m.output)),
				CostMicroUSD: int64(math.Round(m.cents * centsToMicro)),
				OccurredAt:   d,
				WorkspaceRef: companyRef(co.ID),
				Actor:        "agent:" + agentRef(m.agent),
				Gateway:      model.GatewayDirect,
				Provenance:   model.ProvenanceEstimated,
				CostType:     CostType,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergeCosts validates the rows of one day and sums those sharing an agent,
// provider and model, in a stable order. All-zero results are dropped.
func mergeCosts(rows []costRow) ([]dayCost, error) {
	byKey := map[[3]string]*dayCost{}
	for _, r := range rows {
		if !idPattern.MatchString(r.AgentID) || r.Provider == "" || r.Model == "" {
			return nil, shapeErr("cost row identity")
		}
		cents, err := amount(r.CostCents, maxCents, "costCents")
		if err != nil {
			return nil, err
		}
		in, err := amount(r.InputTokens, maxTokens, "inputTokens")
		if err != nil {
			return nil, err
		}
		out, err := amount(r.OutputTokens, maxTokens, "outputTokens")
		if err != nil {
			return nil, err
		}
		k := [3]string{r.AgentID, r.Provider, r.Model}
		m := byKey[k]
		if m == nil {
			m = &dayCost{agent: r.AgentID, provider: r.Provider, model: r.Model}
			byKey[k] = m
		}
		m.cents, m.input, m.output = m.cents+cents, m.input+in, m.output+out
	}
	merged := make([]dayCost, 0, len(byKey))
	for _, m := range byKey {
		if m.cents != 0 || m.input != 0 || m.output != 0 {
			merged = append(merged, *m)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.agent != b.agent {
			return a.agent < b.agent
		}
		if a.provider != b.provider {
			return a.provider < b.provider
		}
		return a.model < b.model
	})
	return merged, nil
}
