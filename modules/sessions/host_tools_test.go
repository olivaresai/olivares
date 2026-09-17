// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// recordingHostToolObserver is the effect-observing adapter: it records every
// call and answers what the test configured.
type recordingHostToolObserver struct {
	mu       sync.Mutex
	calls    int
	drivers  []string
	programs []string
	result   HostToolObservation
	err      error
	during   func(ctx context.Context)
}

func (o *recordingHostToolObserver) ObserveHostTools(ctx context.Context, driver, program string) (HostToolObservation, error) {
	o.mu.Lock()
	o.calls++
	o.drivers = append(o.drivers, driver)
	o.programs = append(o.programs, program)
	during, res, err := o.during, o.result, o.err
	o.mu.Unlock()
	if during != nil {
		during(ctx)
	}
	return res, err
}

func (o *recordingHostToolObserver) set(res HostToolObservation, err error, during func(context.Context)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.result, o.err, o.during = res, err, during
}

func (o *recordingHostToolObserver) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls
}

func hostToolsPath(ref, rawQuery string) string {
	p := "/v1/m/sessions/provider-profiles/" + ref + "/host-tools"
	if rawQuery != "" {
		p += "?" + rawQuery
	}
	return p
}

func (f *readinessFixture) hostTools(token string, tenant model.TenantID, ref, rawQuery string) (SessionHostTools, resp) {
	f.t.Helper()
	return f.hostToolsCtx(context.Background(), token, tenant, ref, rawQuery)
}

func (f *readinessFixture) hostToolsCtx(ctx context.Context, token string, tenant model.TenantID, ref, rawQuery string) (SessionHostTools, resp) {
	f.t.Helper()
	req := httptest.NewRequest("GET", hostToolsPath(ref, rawQuery), nil).WithContext(ctx)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range tenantHdr(tenant) {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.srv.Handler().ServeHTTP(rec, req)
	r := resp{code: rec.Code, raw: rec.Body.String(), header: rec.Header().Clone()}
	_ = json.Unmarshal(rec.Body.Bytes(), &r.body)
	var out SessionHostTools
	if r.code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			f.t.Fatalf("decode host-tools: %v (%s)", err, r.raw)
		}
	}
	return out, r
}

func newHostToolsFixture(t *testing.T, be readinessEngine, obs HostToolObserver, extra ...Option) (*readinessFixture, *inspectingRunner, readinessProgramFixtures) {
	t.Helper()
	bins := newReadinessProgramFixtures(t)
	runner := newInspectingRunner(t)
	opts := []Option{WithRunner(runner), WithProgram(bins.present), WithHostToolObserver(obs)}
	m := New(append(opts, extra...)...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	return newReadinessFixture(t, be, m), runner, bins
}

func TestLaunchHostTools_RouteAuthorizationAndConfinement(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			obs := &recordingHostToolObserver{}
			f, _, bins := newHostToolsFixture(t, be, obs)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

			if _, r := f.hostTools("", f.tenant, ref, ""); r.code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated = %d %s", r.code, r.raw)
			}
			if _, r := f.hostTools(f.viewer, f.other, ref, ""); r.code != http.StatusForbidden {
				t.Fatalf("without sessions:profile:read in that tenant = %d %s", r.code, r.raw)
			}
			if _, r := f.hostTools(f.admin, f.other, ref, ""); r.code != http.StatusNotFound {
				t.Fatalf("cross-tenant profile = %d %s", r.code, r.raw)
			}
			if _, r := f.hostTools(f.viewer, f.tenant, "profile-does-not-exist", ""); r.code != http.StatusNotFound {
				t.Fatalf("unknown profile = %d %s", r.code, r.raw)
			}
			if n := obs.count(); n != 0 {
				t.Fatalf("a refused request reached the observer %d time(s): authorization and confinement come first", n)
			}

			doc, r := f.hostTools(f.viewer, f.tenant, ref, "")
			if r.code != http.StatusOK {
				t.Fatalf("viewer read = %d %s", r.code, r.raw)
			}
			if got := r.header.Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
			if obs.count() != 1 || obs.drivers[0] != "claude" || obs.programs[0] != bins.present {
				t.Fatalf("observer calls=%d drivers=%v programs=%v", obs.count(), obs.drivers, obs.programs)
			}
			if doc.ProfileRef != ref || doc.Driver != "claude" || doc.EnvironmentRef != testEnvRef || doc.EvaluatedEnvironmentRef != testEnvRef {
				t.Fatalf("identity fields: %+v", doc)
			}
			ready, rr := f.readiness(ref, "")
			if rr.code != http.StatusOK || ready.ProfileVersion != doc.ProfileVersion {
				t.Fatalf("profile_version %d does not match the launch-readiness snapshot %d (%d)", doc.ProfileVersion, ready.ProfileVersion, rr.code)
			}
			if ts, err := time.Parse(time.RFC3339, doc.ObservedAt); err != nil || !strings.HasSuffix(doc.ObservedAt, "Z") || ts.IsZero() {
				t.Fatalf("observed_at = %q (%v)", doc.ObservedAt, err)
			}
			// Viewing host tools does not let the viewer launch.
			if r := f.h.doJSON("POST", "/v1/m/sessions/runs", f.viewer,
				map[string]any{"provider_profile_ref": ref}, tenantHdr(f.tenant)); r.code != http.StatusForbidden {
				t.Fatalf("the viewer could launch after reading host tools: %d %s", r.code, r.raw)
			}
		})
	}
}

func TestLaunchHostTools_RefusesEveryRawQuery(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			obs := &recordingHostToolObserver{}
			f, _, _ := newHostToolsFixture(t, be, obs)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
			for _, q := range []string{"x=1", "transport=stream-json", "%zz", "a=1&a=2", "=1", "&", "path=%2Fusr%2Fbin%2Fclaude", "a"} {
				_, r := f.hostTools(f.viewer, f.tenant, ref, q)
				if r.code != http.StatusBadRequest {
					t.Fatalf("query %q = %d %s", q, r.code, r.raw)
				}
				if got := r.header.Get("Cache-Control"); got != "no-store" {
					t.Fatalf("query %q Cache-Control = %q", q, got)
				}
			}
			if n := obs.count(); n != 0 {
				t.Fatalf("a refused query reached the observer %d time(s)", n)
			}
		})
	}
}

func TestLaunchHostTools_EnvironmentLocality(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			obs := &recordingHostToolObserver{}
			obs.set(HostToolObservation{Candidates: []HostToolCandidate{{Origin: "path", Match: "unregistered-observed", Executable: true, Configured: "same"}}}, nil, nil)
			f, _, _ := newHostToolsFixture(t, be, obs)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

			for _, tc := range []struct {
				name, node string
			}{
				{"another environment answers", "env-test-node-b"},
				{"no persistent identity", ""},
			} {
				f.m.UseExecutionEnvironmentRef(tc.node)
				doc, r := f.hostTools(f.viewer, f.tenant, ref, "")
				if r.code != http.StatusOK || doc.State != HostToolsNotChecked || len(doc.Groups) != 0 {
					t.Fatalf("%s: %d %s", tc.name, r.code, r.raw)
				}
				if doc.EvaluatedEnvironmentRef != tc.node || doc.EnvironmentRef != testEnvRef {
					t.Fatalf("%s: environment fields %+v", tc.name, doc)
				}
				if !strings.Contains(r.raw, `"groups":[]`) || !strings.Contains(r.raw, `"evaluated_environment_ref":"`+tc.node+`"`) {
					t.Fatalf("%s: groups or evaluated_environment_ref not always present: %s", tc.name, r.raw)
				}
			}
			if n := obs.count(); n != 0 {
				t.Fatalf("a non-local observation called the observer %d time(s)", n)
			}
			f.m.UseExecutionEnvironmentRef(testEnvRef)
			if doc, r := f.hostTools(f.viewer, f.tenant, ref, ""); r.code != http.StatusOK || doc.State != HostToolsObserved || obs.count() != 1 {
				t.Fatalf("local again: %d %s calls=%d", r.code, r.raw, obs.count())
			}
		})
	}
}

func TestLaunchHostTools_StatesAndClosedGroups(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			// A module with NO observer wired answers unknown.
			nf, _, _ := newHostToolsFixture(t, be, nil)
			cfgHome, userHome := readinessHomes(t)
			nref := nf.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
			if nf.m.HostToolObservationAvailable() {
				t.Fatal("a module without an observer claims one")
			}
			if doc, r := nf.hostTools(nf.viewer, nf.tenant, nref, ""); r.code != http.StatusOK || doc.State != HostToolsUnknown || len(doc.Groups) != 0 {
				t.Fatalf("nil observer: %d %s", r.code, r.raw)
			}
			var typedNil *recordingHostToolObserver
			if New(WithHostToolObserver(typedNil)).HostToolObservationAvailable() {
				t.Fatal("a typed nil observer is treated as wired")
			}

			obs := &recordingHostToolObserver{}
			f, _, _ := newHostToolsFixture(t, freshEngine(t, be), obs)
			cfgHome2, userHome2 := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome2, userHome2, AuthSourceAccountHome)
			leak := "/srv/secret/bin/claude token=SENTINEL-HOST-TOOL-ERROR"
			cands := []HostToolCandidate{
				{Origin: "path", Match: "unregistered-observed", Executable: true, Configured: "same"},
				{Origin: "path", Match: "unregistered-observed", Executable: true, Configured: "same"},
				{Origin: "vendor-default", Match: "unregistered-observed", Executable: true, Configured: "different"},
				{Origin: "managed", Match: "unverified", Executable: true, Configured: "different"},
				{Origin: "future-origin", Match: "sigstore-verified", Executable: false, Configured: "maybe"},
				{Origin: "named", Match: "damaged", Executable: false, Configured: "unknown"},
				{},
			}
			for _, tc := range []struct {
				name   string
				res    HostToolObservation
				err    error
				state  string
				groups []HostToolGroup
			}{
				{name: "observer error", err: errorString(leak), state: HostToolsUnknown, groups: []HostToolGroup{}},
				{name: "unsupported driver", res: HostToolObservation{UnsupportedDriver: true, Candidates: cands}, state: HostToolsUnsupportedDriver, groups: []HostToolGroup{}},
				{name: "no candidate", res: HostToolObservation{}, state: HostToolsNoneObserved, groups: []HostToolGroup{}},
				{name: "distinct groups", res: HostToolObservation{Candidates: cands}, state: HostToolsObserved, groups: []HostToolGroup{
					{Origin: "managed", Match: "unverified", Executable: true, Configured: "different", Count: 1},
					{Origin: "named", Match: "damaged", Executable: false, Configured: "unknown", Count: 1},
					{Origin: "path", Match: "unregistered-observed", Executable: true, Configured: "same", Count: 2},
					{Origin: "unknown", Match: "unknown", Executable: false, Configured: "unknown", Count: 2},
					{Origin: "vendor-default", Match: "unregistered-observed", Executable: true, Configured: "different", Count: 1},
				}},
			} {
				obs.set(tc.res, tc.err, nil)
				doc, r := f.hostTools(f.viewer, f.tenant, ref, "")
				if r.code != http.StatusOK || doc.State != tc.state {
					t.Fatalf("%s: %d %s", tc.name, r.code, r.raw)
				}
				if !reflect.DeepEqual(doc.Groups, tc.groups) {
					t.Fatalf("%s: groups %+v, want %+v", tc.name, doc.Groups, tc.groups)
				}
				sum := 0
				for _, g := range doc.Groups {
					sum += g.Count
				}
				if tc.state == HostToolsObserved && sum != len(cands) {
					t.Fatalf("%s: counts sum to %d, want every candidate (%d)", tc.name, sum, len(cands))
				}
				if strings.Contains(r.raw, "SENTINEL") || strings.Contains(r.raw, "/srv/secret") {
					t.Fatalf("%s: raw error leaked: %s", tc.name, r.raw)
				}
				assertHostToolsShape(t, tc.name, r.raw)
			}
		})
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// assertHostToolsShape pins the exact top-level and group key sets: no path,
// version, hash, note or error field can appear without failing here.
func assertHostToolsShape(t *testing.T, name, raw string) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	want := []string{"driver", "environment_ref", "evaluated_environment_ref", "groups", "observed_at", "profile_ref", "profile_version", "state"}
	if got := hostToolsKeys(top); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: top-level keys %v, want %v", name, got, want)
	}
	var groups []map[string]json.RawMessage
	if err := json.Unmarshal(top["groups"], &groups); err != nil {
		t.Fatalf("%s: groups: %v", name, err)
	}
	for _, g := range groups {
		if got := hostToolsKeys(g); !reflect.DeepEqual(got, []string{"configured", "count", "executable", "match", "origin"}) {
			t.Fatalf("%s: group keys %v", name, got)
		}
	}
}

func hostToolsKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestLaunchHostTools_ProfileChangedDuringObservation(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			obs := &recordingHostToolObserver{}
			f, _, _ := newHostToolsFixture(t, be, obs)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
			obs.set(HostToolObservation{}, nil, func(context.Context) {
				if r := f.h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+ref, f.admin,
					map[string]any{"display_name": "renamed during observation"}, tenantHdr(f.tenant)); r.code != http.StatusOK {
					t.Errorf("patch during observation = %d %s", r.code, r.raw)
				}
			})
			_, r := f.hostTools(f.viewer, f.tenant, ref, "")
			if r.code != http.StatusConflict {
				t.Fatalf("straddled observation = %d %s", r.code, r.raw)
			}
			if got := r.header.Get("Cache-Control"); got != "no-store" {
				t.Fatalf("409 Cache-Control = %q", got)
			}
			errObj, _ := r.body["error"].(map[string]any)
			if errObj["code"] != codeProfileChanged {
				t.Fatalf("409 body lacks the typed code: %s", r.raw)
			}
			if strings.Contains(r.raw, `"groups"`) {
				t.Fatalf("a straddled observation was published: %s", r.raw)
			}
		})
	}
}

func TestLaunchHostTools_HasNoEffectsAndPassesTheWiredProgram(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			obs := &recordingHostToolObserver{}
			codexBins := newReadinessProgramFixtures(t)
			f, runner, bins := newHostToolsFixture(t, be, obs,
				WithCredentialSource(mintRefusingCredentialSource{t}),
				WithProviderDriver(NewCodexDriver()), WithDriverProgram("codex", codexBins.present),
				WithProviderCredentialSource("codex", mintRefusingProviderSource{t}),
				WithLaunchGate(refusingLaunchGate{t}),
				WithStopGate(refusingStopGate{t}),
				WithProviderApprovalGate(refusingApprovalGate{t}),
			)
			cfgHome, userHome := readinessHomes(t)
			claudeRef := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
			cfgHome2, userHome2 := readinessHomes(t)
			codexRef := f.createProfile("codex", cfgHome2, userHome2, AuthSourceAccountHome)
			cfgHome3, userHome3 := readinessHomes(t)
			grokRef := f.createProfile("grok", cfgHome3, userHome3, AuthSourceAccountHome)

			programs := map[string]string{}
			for k, v := range f.m.rt.driverPrograms {
				programs[k] = v
			}
			program, drivers := f.m.rt.program, len(f.m.rt.drivers)
			inspections := runner.inspections()
			before := tableCounts(t, be)
			for _, ref := range []string{claudeRef, codexRef, grokRef, claudeRef} {
				if _, r := f.hostTools(f.viewer, f.tenant, ref, ""); r.code != http.StatusOK {
					t.Fatalf("read %s = %d %s", ref, r.code, r.raw)
				}
			}
			if diff := diffCounts(before, tableCounts(t, be)); len(diff) != 0 {
				t.Fatalf("the read wrote to the store: %v", diff)
			}
			if runner.inspections() != inspections {
				t.Fatal("the read inspected or launched through the runner")
			}
			if !reflect.DeepEqual(programs, f.m.rt.driverPrograms) || program != f.m.rt.program || drivers != len(f.m.rt.drivers) {
				t.Fatal("the read changed the configured programs or the registered drivers")
			}
			wantDrivers := []string{"claude", "codex", "grok", "claude"}
			wantPrograms := []string{bins.present, codexBins.present, "", bins.present}
			if !reflect.DeepEqual(obs.drivers, wantDrivers) || !reflect.DeepEqual(obs.programs, wantPrograms) {
				t.Fatalf("observer received drivers=%v programs=%v, want %v %v", obs.drivers, obs.programs, wantDrivers, wantPrograms)
			}
			// An unregistered non-Claude driver has no configured program; it never
			// borrows the Claude program.
			if got := f.m.hostToolProgram("grok"); got != "" {
				t.Fatalf("unregistered grok program = %q", got)
			}
		})
	}
}

func TestLaunchHostTools_CancellationIsNeverPublished(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			obs := &recordingHostToolObserver{}
			f, _, _ := newHostToolsFixture(t, be, obs)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
			obs.set(HostToolObservation{Candidates: []HostToolCandidate{{Origin: "path", Match: "unregistered-observed", Executable: true, Configured: "same"}}}, nil, nil)

			// Service level: the observer's context is the caller's, and a cancel
			// during observation yields no document.
			ctx, cancel := context.WithCancel(context.Background())
			var sawLive, sawCanceled bool
			obs.set(obs.result, nil, func(c context.Context) {
				sawLive = c.Err() == nil
				cancel()
				sawCanceled = c.Err() != nil
			})
			out, err := f.m.EvaluateHostTools(ctx, f.tenant, ref)
			// The typed refusal, not whatever a later store read happens to return
			// on a canceled context: the observation itself is what was canceled.
			if err != errHostToolsCanceled || out.State != "" || !sawLive || !sawCanceled {
				t.Fatalf("canceled observation: out=%+v err=%v live=%v canceled=%v", out, err, sawLive, sawCanceled)
			}

			// HTTP level: the same cancel through the real router is the 503 refusal.
			hctx, hcancel := context.WithCancel(context.Background())
			obs.set(obs.result, nil, func(context.Context) { hcancel() })
			_, r := f.hostToolsCtx(hctx, f.viewer, f.tenant, ref, "")
			if r.code != http.StatusServiceUnavailable || strings.Contains(r.raw, `"groups"`) {
				t.Fatalf("a canceled request was not refused: %d %s", r.code, r.raw)
			}
			if got := r.header.Get("Cache-Control"); got != "no-store" {
				t.Fatalf("canceled Cache-Control = %q", got)
			}
		})
	}
}
