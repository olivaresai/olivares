// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeHost stands in for every owner first boot reaches. Its effects are idempotent: the
// host keeps one effect per stage however often a stage is applied.
type fakeHost struct {
	applies  map[Stage]int
	verifies map[Stage]int
	effects  map[Stage]Effect
	order    []Stage
	health   bool
	identity bool
	measured error
}

func newFakeHost() *fakeHost {
	return &fakeHost{applies: map[Stage]int{}, verifies: map[Stage]int{}, effects: map[Stage]Effect{}, health: true, identity: true}
}

type fakeStep struct {
	host  *fakeHost
	stage Stage
}

func (s fakeStep) Apply(_ context.Context, in Input) (Effect, error) {
	s.host.applies[s.stage]++
	s.host.order = append(s.host.order, s.stage)
	s.host.effects[s.stage] = Effect(string(s.stage) + " for " + in.Answers.Hostname)
	return s.host.effects[s.stage], nil
}

func (s fakeStep) Verify(_ context.Context, _ Input, recorded Effect) error {
	s.host.verifies[s.stage]++
	if s.host.effects[s.stage] != recorded {
		return Refuse(string(s.stage) + " differs from its record")
	}
	return nil
}

func (h *fakeHost) Measure(context.Context, Input) (Measurement, error) {
	return Measurement{Health: h.health, Identity: h.identity, Detail: "fake measurement"}, h.measured
}

func (h *fakeHost) seams() Seams {
	return Seams{
		Identity: fakeStep{h, StageIdentity}, HostSettings: fakeStep{h, StageHostSettings},
		ProductConfig: fakeStep{h, StageProductConfig}, Storage: fakeStep{h, StageStorage},
		SetupDelivery: fakeStep{h, StageSetupDelivery}, Firewall: fakeStep{h, StageFirewall},
		StartServices: fakeStep{h, StageStartServices}, Readiness: h,
	}
}

func fixedNow() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }

func answersFixture(t *testing.T, hostname string) Input {
	t.Helper()
	doc, err := os.ReadFile("../../answers/testdata/cloud-init.json")
	if err != nil {
		t.Fatal(err)
	}
	doc = bytes.Replace(doc, []byte(`"hostname": "olivares.example.test"`), []byte(`"hostname": "`+hostname+`"`), 1)
	in, err := NewInput("file:/etc/olivares-appliance/answers.json", "", doc)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func newMachine(dir string, h *fakeHost, in *Input, seams Seams) *Machine {
	return &Machine{
		Store:      Store{Dir: dir},
		Load:       func(context.Context) (Input, error) { return *in, nil },
		Identities: func() ([]string, error) { return nil, nil },
		Seams:      seams,
		Log:        io.Discard,
		Now:        fixedNow,
	}
}

func readyMarker(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, readyFile))
	return err == nil
}

// effectful are the stages that reach the host through a Step.
var effectful = Stages[1 : len(Stages)-1]

func TestStageMachine_PersistsEachCompletedStageAtomicallyAndComparesOnRestart(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	for i, killed := range effectful {
		t.Run("killed after "+string(killed)+" was recorded", func(t *testing.T) {
			dir, h := t.TempDir(), newFakeHost()
			first := newMachine(dir, h, &in, h.seams())
			first.crash = func(s Stage, b boundary) bool { return s == killed && b == afterPersist }
			if _, err := first.Run(context.Background()); !errors.Is(err, errCrashed) {
				t.Fatalf("first run: %v", err)
			}
			// A write interrupted before its rename leaves only a temporary file behind.
			if err := os.WriteFile(filepath.Join(dir, "."+recordFile+".interrupted"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			rec, err := newMachine(dir, h, &in, h.seams()).Run(context.Background())
			if err != nil || rec.State != Ready {
				t.Fatalf("resumed run: %+v %v", rec, err)
			}
			for j, s := range effectful {
				if h.applies[s] != 1 {
					t.Fatalf("%s applied %d times", s, h.applies[s])
				}
				if want := map[bool]int{true: 1, false: 0}[j <= i]; h.verifies[s] != want {
					t.Fatalf("%s compared %d times on restart, want %d", s, h.verifies[s], want)
				}
			}
			if !readyMarker(dir) {
				t.Fatal("no ready marker after the resumed run")
			}
		})
	}
	t.Run("killed between an effect and its record", func(t *testing.T) {
		dir, h := t.TempDir(), newFakeHost()
		first := newMachine(dir, h, &in, h.seams())
		first.crash = func(s Stage, b boundary) bool { return s == StageProductConfig && b == afterEffect }
		if _, err := first.Run(context.Background()); !errors.Is(err, errCrashed) {
			t.Fatalf("first run: %v", err)
		}
		rec, found, err := Store{Dir: dir}.Load()
		if err != nil || !found || rec.State != Applying || rec.Stage != StageProductConfig {
			t.Fatalf("interrupted record: %+v %v %v", rec, found, err)
		}
		if _, err := newMachine(dir, h, &in, h.seams()).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		// The unrecorded effect is applied once more; the host keeps one effect.
		if h.applies[StageProductConfig] != 2 || h.applies[StageIdentity] != 1 || h.applies[StageStorage] != 1 {
			t.Fatalf("applies after resume: %v", h.applies)
		}
	})
	t.Run("changed answers refuse and name the last completed stage", func(t *testing.T) {
		dir, h := t.TempDir(), newFakeHost()
		first := newMachine(dir, h, &in, h.seams())
		first.crash = func(s Stage, b boundary) bool { return s == StageStorage && b == afterPersist }
		if _, err := first.Run(context.Background()); !errors.Is(err, errCrashed) {
			t.Fatalf("first run: %v", err)
		}
		changed := answersFixture(t, "changed.example.test")
		rec, err := newMachine(dir, h, &changed, h.seams()).Run(context.Background())
		if err != nil || rec.State != Refused || rec.Stage != StageValidate || !strings.Contains(rec.Reason, string(StageStorage)) {
			t.Fatalf("changed answers: %+v %v", rec, err)
		}
		if strings.Contains(rec.Reason, "changed.example.test") || rec.Digest != in.Digest {
			t.Fatalf("refusal must keep the recorded digest and quote no value: %+v", rec)
		}
		if h.applies[StageSetupDelivery] != 0 || len(rec.Completed) != 4 {
			t.Fatalf("effects after the refusal: %v, completed %v", h.applies, rec.Completed)
		}
		if readyMarker(dir) {
			t.Fatal("a refused first boot wrote the ready marker")
		}
	})
}

func TestStageMachine_ReadyRequiresProductHealthAndIdentityChecksNotASystemctlExit(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	cases := []struct {
		name             string
		health, identity bool
		measured         error
		want             State
	}{
		{"service started, product not ready", false, true, nil, Pending},
		{"product ready with a foreign identity", true, false, nil, Refused},
		{"readiness unmeasured", false, false, Wait("the readiness endpoint cannot be reached"), Pending},
		{"readiness adapter failed", true, true, errors.New("probe exploded"), Refused},
		{"product health and identity measured", true, true, nil, Ready},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, h := t.TempDir(), newFakeHost()
			h.health, h.identity, h.measured = tc.health, tc.identity, tc.measured
			rec, err := newMachine(dir, h, &in, h.seams()).Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if h.applies[StageStartServices] != 1 {
				t.Fatal("the service start, a successful systemctl call here, did not happen")
			}
			if rec.State != tc.want || readyMarker(dir) != (tc.want == Ready) {
				t.Fatalf("state %s (marker %v), want %s: %+v", rec.State, readyMarker(dir), tc.want, rec)
			}
			if tc.want != Ready && rec.Stage != StageReadiness {
				t.Fatalf("not-ready outcome names stage %q", rec.Stage)
			}
		})
	}
}

func TestFirewallPrerequisite_IsMeasuredBeforeAnyNonLoopbackExposureAndUnmeasuredRefuses(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	if slices.Index(Stages, StageFirewall) > slices.Index(Stages, StageStartServices) {
		t.Fatal("the firewall stage runs after services are exposed")
	}
	dir, h := t.TempDir(), newFakeHost()
	seams := h.seams()
	seams.Firewall = UnmeasuredFirewall{}
	for run := 0; run < 2; run++ {
		rec, err := newMachine(dir, h, &in, seams).Run(context.Background())
		if err != nil || rec.State != Refused || rec.Stage != StageFirewall {
			t.Fatalf("run %d with an unmeasured firewall: %+v %v", run, rec, err)
		}
		if h.applies[StageStartServices] != 0 || readyMarker(dir) {
			t.Fatalf("run %d exposed the product before the firewall was measured", run)
		}
	}
	measured := newFakeHost()
	rec, err := newMachine(t.TempDir(), measured, &in, measured.seams()).Run(context.Background())
	if err != nil || rec.State != Ready {
		t.Fatalf("with a measured firewall: %+v %v", rec, err)
	}
	if slices.Index(measured.order, StageFirewall) > slices.Index(measured.order, StageStartServices) {
		t.Fatalf("services started before the firewall measurement: %v", measured.order)
	}
}

func TestTokenDelivery_TheSeamRefusesByDefaultAndNeverWritesPlaintextToTheJournal(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	const plaintext = "olst_" + "FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"
	sink := filepath.Join(t.TempDir(), "setup-token")
	cases := []struct {
		name  string
		seam  Step
		state State
	}{
		{"the default seam refuses", RefusingSetupDelivery{}, Refused},
		{"an adapter error quoting the token", leakyDelivery{}, Refused},
		{"a token delivered to its protected sink", sinkDelivery{path: sink, token: plaintext}, Ready},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, h := t.TempDir(), newFakeHost()
			seams := h.seams()
			seams.SetupDelivery = tc.seam
			var journal bytes.Buffer
			m := newMachine(dir, h, &in, seams)
			m.Log = &journal
			rec, err := m.Run(context.Background())
			if err != nil || rec.State != tc.state {
				t.Fatalf("%+v %v", rec, err)
			}
			if tc.state == Refused && (rec.Stage != StageSetupDelivery || h.applies[StageStartServices] != 0) {
				t.Fatalf("the product started without a protected delivery: %+v", rec)
			}
			record, err := os.ReadFile(filepath.Join(dir, recordFile))
			if err != nil {
				t.Fatal(err)
			}
			for _, out := range []string{journal.String(), string(record)} {
				if strings.Contains(out, "olst_") || strings.Contains(out, "FAKEFAKE") {
					t.Fatalf("token plaintext reached the journal or the record:\n%s", out)
				}
			}
		})
	}
	if info, err := os.Stat(sink); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the sink is not a protected file: %v", err)
	}
}

// leakyDelivery fails the way a careless adapter would: quoting the secret it handled.
type leakyDelivery struct{}

func (leakyDelivery) Apply(context.Context, Input) (Effect, error) {
	return "", errors.New("could not deliver olst_FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE")
}

func (leakyDelivery) Verify(context.Context, Input, Effect) error { return nil }

// sinkDelivery is what the setup-token owner's seam will do: write the token to a protected
// sink and report where, not what.
type sinkDelivery struct{ path, token string }

func (s sinkDelivery) Apply(context.Context, Input) (Effect, error) {
	if err := os.WriteFile(s.path, []byte(s.token+"\n"), 0o600); err != nil {
		return "", err
	}
	return Effect("setup token delivered to " + s.path), nil
}

func (s sinkDelivery) Verify(context.Context, Input, Effect) error { return nil }
