// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"reflect"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// The host-tools READ (HC1 revision 1): which official provider CLI candidates
// this node observes for ONE provider profile, grouped into closed codes.
//
// ⛔ IT IS ADVISORY AND GRANTS NOTHING. It never pins a program, registers a
// driver, installs, probes or launches, and it does not change launch readiness.
// It lives under the same permission, tenant confinement and environment
// identity as launch-readiness because it explains the same profile.
//
// ⛔ AND IT CARRIES NO PATH. Discovery, deduplication and file identity stay
// inside the adapter behind HostToolObserver; this module receives closed codes
// only, so neither the handler nor the console can leak what it never holds.

// HostToolObserver observes the official CLI candidates of one driver on THIS
// node. configuredProgram is the effective program from immutable runtime wiring
// ("" when the driver has none). An implementation must not execute, fetch,
// write or register anything, must honor ctx, and must not return a path, an
// argv, an environment value or a raw OS error inside the observation.
type HostToolObserver interface {
	ObserveHostTools(ctx context.Context, driver, configuredProgram string) (HostToolObservation, error)
}

// HostToolObservation is one adapter answer. UnsupportedDriver means the
// adapter has no detector for the driver; candidates are then ignored.
type HostToolObservation struct {
	UnsupportedDriver bool
	Candidates        []HostToolCandidate
}

// HostToolCandidate is one deduplicated detected candidate, as codes. Codes this
// module does not recognize are published as unknown, never as a stronger fact.
type HostToolCandidate struct {
	Origin     string
	Match      string
	Executable bool
	// Configured is same, different or unknown: whether this candidate is the
	// file the effective configured program resolves to.
	Configured string
}

// The closed states of the read.
const (
	HostToolsObserved          = "observed"
	HostToolsNoneObserved      = "none_observed"
	HostToolsUnsupportedDriver = "unsupported_driver"
	HostToolsUnknown           = "unknown"
	HostToolsNotChecked        = "not_checked_in_this_environment"
)

// The closed group vocabularies. unknown is shared by all three.
const (
	HostToolOriginManaged       = "managed"
	HostToolOriginVendorDefault = "vendor-default"
	HostToolOriginPath          = "path"
	HostToolOriginNamed         = "named"

	HostToolMatchRegistered           = "registered"
	HostToolMatchManifestCorroborated = "manifest-corroborated"
	HostToolMatchUnregisteredObserved = "unregistered-observed"
	HostToolMatchUnverified           = "unverified"
	HostToolMatchDamaged              = "damaged"

	HostToolConfiguredSame      = "same"
	HostToolConfiguredDifferent = "different"

	HostToolCodeUnknown = "unknown"
)

// HostToolGroup counts the candidates that share one closed tuple.
type HostToolGroup struct {
	Origin     string `json:"origin"`
	Match      string `json:"match"`
	Executable bool   `json:"executable"`
	Configured string `json:"configured"`
	Count      int    `json:"count"`
}

// SessionHostTools is the complete response. Every field is a reference, a
// closed code, a count or a timestamp.
type SessionHostTools struct {
	ProfileRef     string `json:"profile_ref"`
	ProfileVersion int64  `json:"profile_version"`
	Driver         string `json:"driver"`
	EnvironmentRef string `json:"environment_ref"`
	// EvaluatedEnvironmentRef is always present; "" when this node has no
	// persistent execution-environment identity.
	EvaluatedEnvironmentRef string          `json:"evaluated_environment_ref"`
	ObservedAt              string          `json:"observed_at"`
	State                   string          `json:"state"`
	Groups                  []HostToolGroup `json:"groups"`
}

// WithHostToolObserver wires the host-tools adapter. nil leaves the read
// answering unknown.
func WithHostToolObserver(o HostToolObserver) Option {
	return func(m *Module) {
		if nilHostToolObserver(o) {
			m.rt.hostTools = nil
			return
		}
		m.rt.hostTools = o
	}
}

// HostToolObservationAvailable reports whether an adapter is wired. It exists
// for the composition root's own battery.
func (m *Module) HostToolObservationAvailable() bool { return !nilHostToolObserver(m.rt.hostTools) }

// nilHostToolObserver also catches a typed nil pointer, which would otherwise
// pass the interface nil check and panic on the first request.
func nilHostToolObserver(o HostToolObserver) bool {
	if o == nil {
		return true
	}
	v := reflect.ValueOf(o)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return v.IsNil()
	}
	return false
}

var errHostToolsUnavailable = &runErr{
	http.StatusServiceUnavailable,
	"the provider profile store is not available on this node; host tools were not observed",
}

// errHostToolsCanceled is returned instead of a document when the request was
// canceled during the observation: a canceled observation is never published.
var errHostToolsCanceled = &runErr{
	http.StatusServiceUnavailable,
	"the request was canceled before the host-tool observation completed; nothing was published",
}

// EvaluateHostTools is the whole service behind the GET. The order mirrors
// EvaluateLaunchReadiness: read the profile, observe without a database
// transaction, read the profile again, and refuse a straddled observation with
// the same typed 409.
func (m *Module) EvaluateHostTools(ctx context.Context, tenant model.TenantID, ref string) (SessionHostTools, error) {
	if m.data == nil {
		return SessionHostTools{}, errHostToolsUnavailable
	}
	before, err := m.GetProfile(ctx, tenant, ref)
	if err != nil {
		return SessionHostTools{}, err
	}
	out := m.observeHostTools(ctx, before)
	if ctx.Err() != nil {
		return SessionHostTools{}, errHostToolsCanceled
	}
	after, err := m.GetProfile(ctx, tenant, ref)
	if err != nil {
		return SessionHostTools{}, err
	}
	if after.Version != before.Version {
		return SessionHostTools{}, errReadinessProfileChanged
	}
	if ctx.Err() != nil {
		return SessionHostTools{}, errHostToolsCanceled
	}
	return out, nil
}

// observeHostTools evaluates ONE profile snapshot.
func (m *Module) observeHostTools(ctx context.Context, prof ProviderProfile) (out SessionHostTools) {
	env := m.rt.environmentRef
	out = SessionHostTools{
		ProfileRef: prof.Ref, ProfileVersion: prof.Version, Driver: prof.Driver,
		EnvironmentRef: prof.EnvironmentRef, EvaluatedEnvironmentRef: env,
		State: HostToolsUnknown, Groups: []HostToolGroup{},
	}
	defer func() { out.ObservedAt = model.NewTimestamp(m.now()).String() }()
	// Another node's profile, or a node with no identity: this filesystem says
	// nothing about it, and the adapter is not asked.
	if env == "" || prof.EnvironmentRef != env {
		out.State = HostToolsNotChecked
		return out
	}
	obs := m.rt.hostTools
	if nilHostToolObserver(obs) {
		return out
	}
	driver, err := normalizeDriverKey(prof.Driver)
	if err != nil {
		return out
	}
	res, err := obs.ObserveHostTools(ctx, driver, m.hostToolProgram(driver))
	if err != nil {
		// The raw error never leaves this function.
		return out
	}
	switch {
	case res.UnsupportedDriver:
		out.State = HostToolsUnsupportedDriver
	case len(res.Candidates) == 0:
		out.State = HostToolsNoneObserved
	default:
		out.State = HostToolsObserved
		out.Groups = groupHostToolCandidates(res.Candidates)
	}
	return out
}

// hostToolProgram is the effective configured program of a driver, from the same
// immutable wiring a launch uses. A non-Claude driver this runtime has not
// registered has no configured program, so its comparison stays unknown instead
// of borrowing the Claude program.
func (m *Module) hostToolProgram(driver string) string {
	if d, ok := m.driverFor(driver); ok {
		return m.driverProgram(d)
	}
	if driver == providerDriverClaude {
		return m.rt.program
	}
	return ""
}

// groupHostToolCandidates counts every candidate under its closed tuple and
// sorts the groups by that tuple. Nothing is dropped: the counts sum to the
// number of candidates.
func groupHostToolCandidates(cands []HostToolCandidate) []HostToolGroup {
	counts := map[HostToolGroup]int{}
	for _, c := range cands {
		key := HostToolGroup{
			Origin:     closedHostToolCode(c.Origin, HostToolOriginManaged, HostToolOriginVendorDefault, HostToolOriginPath, HostToolOriginNamed),
			Match:      closedHostToolCode(c.Match, HostToolMatchRegistered, HostToolMatchManifestCorroborated, HostToolMatchUnregisteredObserved, HostToolMatchUnverified, HostToolMatchDamaged),
			Executable: c.Executable,
			Configured: closedHostToolCode(c.Configured, HostToolConfiguredSame, HostToolConfiguredDifferent),
		}
		counts[key]++
	}
	groups := make([]HostToolGroup, 0, len(counts))
	for key, n := range counts {
		key.Count = n
		groups = append(groups, key)
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.Origin != b.Origin {
			return a.Origin < b.Origin
		}
		if a.Match != b.Match {
			return a.Match < b.Match
		}
		if a.Executable != b.Executable {
			return !a.Executable
		}
		return a.Configured < b.Configured
	})
	return groups
}

func closedHostToolCode(code string, known ...string) string {
	for _, k := range known {
		if code == k {
			return code
		}
	}
	return HostToolCodeUnknown
}

// handleProfileHostTools reports the official provider CLI candidates this node observes for one provider profile, grouped into closed codes; it installs nothing, probes nothing and authorizes nothing.
func (m *Module) handleProfileHostTools(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// Before any branch: every answer of this handler is a dated observation.
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		writeReadinessErr(w, badRequest("this route accepts no query parameters"))
		return
	}
	out, err := m.EvaluateHostTools(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeReadinessErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
