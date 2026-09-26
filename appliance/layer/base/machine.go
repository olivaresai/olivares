// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package base runs an appliance's first boot as ordered stages. Each stage reaches the host
// through a seam, persists its effect atomically and, when first boot runs again, is compared
// with the host instead of being repeated. Seams whose owner has not shipped refuse by default,
// and first boot is ready only when the product's own readiness and identity checks pass.
package base

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// State is the reported first-boot status.
type State string

// First-boot states.
const (
	// Pending waits for something expected to happen without changing the inputs.
	Pending State = "pending"
	// Applying is recorded while a stage runs; after an interruption the next run resumes.
	Applying State = "applying"
	// Ready means every stage completed and the product's readiness was measured.
	Ready State = "ready"
	// Refused needs an operator or an owner to act; the reason says what.
	Refused State = "refused"
)

// Stage names one step of first boot.
type Stage string

// First-boot stages, in the order of Stages.
const (
	StageValidate      Stage = "validate"
	StageIdentity      Stage = "prepare-identity"
	StageHostSettings  Stage = "verify-host-settings"
	StageProductConfig Stage = "generate-product-config"
	StageStorage       Stage = "initialize-storage"
	StageSetupDelivery Stage = "prepare-setup-delivery"
	StageFirewall      Stage = "verify-firewall"
	StageStartServices Stage = "start-services"
	StageReadiness     Stage = "measure-readiness"
)

// Stages is the order of first boot. The firewall stage precedes the first stage that
// exposes a service beyond loopback.
var Stages = []Stage{
	StageValidate, StageIdentity, StageHostSettings, StageProductConfig, StageStorage,
	StageSetupDelivery, StageFirewall, StageStartServices, StageReadiness,
}

// Outcome ends a stage without completing it. Its reason is fixed text chosen by the
// adapter: it never carries answers values, adapter output or secrets.
type Outcome struct {
	State  State
	Reason string
}

func (o *Outcome) Error() string { return string(o.State) + ": " + o.Reason }

// Refuse reports a condition that needs an operator or an owner to act.
func Refuse(reason string) error { return &Outcome{State: Refused, Reason: reason} }

// Wait reports a condition expected to clear without changing the inputs.
func Wait(reason string) error { return &Outcome{State: Pending, Reason: reason} }

// Effect identifies the observable result of a completed stage. It holds no secret.
type Effect string

// Step is the seam of one stage. Apply must be idempotent: a run interrupted after the
// effect and before its record was persisted applies it again. Verify compares a recorded
// effect with the host and returns an Outcome when they differ.
type Step interface {
	Apply(ctx context.Context, in Input) (Effect, error)
	Verify(ctx context.Context, in Input, recorded Effect) error
}

// Measurement is the product's readiness as its own checks report it.
type Measurement struct {
	Health   bool   // the product's readiness endpoint answered ok
	Identity bool   // the identity it serves and stores is this instance's
	Detail   string // fixed text for the record
}

// Readiness measures the product after its service was started.
type Readiness interface {
	Measure(ctx context.Context, in Input) (Measurement, error)
}

// Seams connects each stage to its owner. A seam left nil, the zero value included, refuses
// with its stage's name before any stage runs.
type Seams struct {
	Identity      Step
	HostSettings  Step
	ProductConfig Step
	Storage       Step
	SetupDelivery Step
	Firewall      Step
	StartServices Step
	Readiness     Readiness
}

func (s Seams) step(stage Stage) Step {
	switch stage {
	case StageIdentity:
		return s.Identity
	case StageHostSettings:
		return s.HostSettings
	case StageProductConfig:
		return s.ProductConfig
	case StageStorage:
		return s.Storage
	case StageSetupDelivery:
		return s.SetupDelivery
	case StageFirewall:
		return s.Firewall
	case StageStartServices:
		return s.StartServices
	}
	return nil
}

// missing refuses an incomplete composition: the answers loader and the identity inspection,
// which also reach the host, and then every seam in first-boot order.
func (m *Machine) missing() (Stage, string, bool) {
	switch {
	case m.Load == nil:
		return StageValidate, "no answers loader is installed", true
	case m.Identities == nil:
		return StageValidate, "no product identity inspection is installed", true
	}
	if stage, ok := m.Seams.missing(); ok {
		return stage, "no " + string(stage) + " seam is installed", true
	}
	return "", "", false
}

// missing returns the first stage, in first-boot order, whose seam is not installed.
func (s Seams) missing() (Stage, bool) {
	for _, stage := range Stages[1 : len(Stages)-1] {
		if s.step(stage) == nil {
			return stage, true
		}
	}
	if s.Readiness == nil {
		return StageReadiness, true
	}
	return "", false
}

// Machine applies first boot. Run it under the store's lock.
type Machine struct {
	Store Store
	// Load resolves and validates the answers; it returns an Outcome when there are none
	// yet or they are refused.
	Load func(ctx context.Context) (Input, error)
	// Identities lists the product identities present on the host.
	Identities func() ([]string, error)
	Seams      Seams
	Log        io.Writer // the unit's standard output, which is the journal
	Now        func() time.Time

	// crash, set by tests, stops a run at a boundary as if the process were killed there.
	crash func(Stage, boundary) bool
}

type boundary int

const (
	afterEffect boundary = iota
	afterPersist
)

var errCrashed = errors.New("run stopped at a test boundary")

// Run applies first boot from wherever the record says it stopped and returns the new
// record. An error means the record could not be read or written; every other outcome,
// including refusals, is in the record.
func (m *Machine) Run(ctx context.Context) (Record, error) {
	rec, found, err := m.Store.Load()
	if err != nil {
		return Record{}, err
	}
	if found && rec.State == Ready {
		return rec, m.Store.MarkReady(rec)
	}
	rec.Schema = recordSchema
	// An incomplete composition refuses before any stage runs: it can neither panic nor start
	// the product.
	if stage, reason, ok := m.missing(); ok {
		return m.finish(rec, stage, Refuse(reason+"; first boot applies nothing without it"))
	}
	in, err := m.Load(ctx)
	if err != nil {
		var outcome *Outcome
		if found && errors.As(err, &outcome) && outcome.State == Pending && rec.Stage != "" && rec.Stage != StageValidate {
			// The carrier is gone, not changed: keep the outcome the last run recorded.
			m.logf("%s: %s; the recorded %s outcome is kept", StageValidate, outcome.Reason, rec.Stage)
			return rec, nil
		}
		return m.finish(rec, StageValidate, err)
	}
	if rec.Digest != "" && rec.Digest != in.Digest {
		recovery := "run `appliance-firstboot reconcile` to apply them before the product starts, or restore them"
		if mayHaveStarted(rec) {
			recovery = "restore the answers the record was built from: first boot does not reconfigure a started product"
		}
		return m.finish(rec, StageValidate, Refuse("the answers changed after "+string(lastCompleted(rec))+
			" completed; "+recovery))
	}
	if !mayHaveStarted(rec) {
		ids, err := m.Identities()
		if err != nil {
			return m.finish(rec, StageValidate, Refuse("the product identities cannot be inspected"))
		}
		if len(ids) > 0 {
			return m.finish(rec, StageValidate, Refuse("product identities exist before first boot started the product ("+
				strings.Join(ids, ", ")+"); an initialized installation is never treated as a template"))
		}
	}
	rec.Source = in.Source
	for _, stage := range Stages[1 : len(Stages)-1] {
		step := m.Seams.step(stage)
		if recorded, ok := effectOf(rec, stage); ok {
			if err := step.Verify(ctx, in, recorded); err != nil {
				return m.finish(rec, stage, err)
			}
			m.logf("%s: recorded effect verified", stage)
			continue
		}
		rec.State, rec.Stage, rec.Reason = Applying, stage, ""
		if stage == StageStartServices {
			// Recorded with Applying, before the start can be queued; never cleared.
			rec.ProductMayHaveStarted = true
		}
		if err := m.save(&rec); err != nil {
			return rec, err
		}
		effect, err := step.Apply(ctx, in)
		if err != nil {
			return m.finish(rec, stage, err)
		}
		if m.crash != nil && m.crash(stage, afterEffect) {
			return rec, errCrashed
		}
		rec.Completed = append(rec.Completed, Completed{Stage: stage, Effect: effect})
		if rec.Digest == "" && bindsAnswers(stage) {
			// The digest is recorded with the first stage that depends on the answers.
			rec.Digest = in.Digest
		}
		if err := m.save(&rec); err != nil {
			return rec, err
		}
		m.logf("%s: applied", stage)
		if m.crash != nil && m.crash(stage, afterPersist) {
			return rec, errCrashed
		}
	}
	return m.measure(ctx, rec, in)
}

// measure commits ready only on the product's own health and identity checks: a started
// service, or a systemctl call that exited 0, is not readiness.
func (m *Machine) measure(ctx context.Context, rec Record, in Input) (Record, error) {
	got, err := m.Seams.Readiness.Measure(ctx, in)
	if err != nil {
		return m.finish(rec, StageReadiness, err)
	}
	if !got.Health {
		return m.finish(rec, StageReadiness, Wait("the product has not reported ready: "+got.Detail))
	}
	if !got.Identity {
		return m.finish(rec, StageReadiness, Refuse("the product identity check failed: "+got.Detail))
	}
	rec.Completed = append(rec.Completed, Completed{Stage: StageReadiness, Effect: Effect("measured: " + got.Detail)})
	rec.State, rec.Stage, rec.Reason = Ready, "", ""
	if err := m.save(&rec); err != nil {
		return rec, err
	}
	m.logf("first boot is ready")
	return rec, m.Store.MarkReady(rec)
}

// finish records a stage that did not complete. Only an Outcome's fixed reason reaches the
// record and the journal; any other error is named by its stage alone, because adapter
// error text may quote what the adapter handled.
func (m *Machine) finish(rec Record, stage Stage, err error) (Record, error) {
	state, reason := Refused, "the "+string(stage)+" adapter failed"
	var outcome *Outcome
	if errors.As(err, &outcome) {
		state, reason = outcome.State, outcome.Reason
	}
	rec.State, rec.Stage, rec.Reason = state, stage, reason
	if err := m.save(&rec); err != nil {
		return rec, err
	}
	m.logf("%s: %s: %s", stage, state, reason)
	return rec, nil
}

func (m *Machine) save(rec *Record) error {
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	rec.Observed = now().UTC().Format(time.RFC3339)
	return m.Store.Save(*rec)
}

func (m *Machine) logf(format string, args ...any) {
	if m.Log != nil {
		_, _ = fmt.Fprintf(m.Log, "first boot: "+format+"\n", args...)
	}
}

// Reconcile is the recovery from changed answers, and from a recorded effect the host no longer
// matches, before the product starts. It forgets the digest and every recorded stage that
// depends on the answers, keeping prepare-identity and the instance identity it recorded, so
// the next Run verifies and applies those stages again with the current answers and host.
// Once the product may have started it refuses and changes nothing: first boot does not
// reconfigure a started product.
func (m *Machine) Reconcile() (Record, error) {
	rec, found, err := m.Store.Load()
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, Refuse("first boot has recorded nothing to reconcile")
	}
	if rec.State == Ready || mayHaveStarted(rec) {
		return rec, Refuse("the product may have started and first boot does not reconfigure it; " +
			"restore the answers or the file the record was built from")
	}
	kept := make([]Completed, 0, len(rec.Completed))
	for _, c := range rec.Completed {
		if !bindsAnswers(c.Stage) {
			kept = append(kept, c)
		}
	}
	rec.Completed, rec.Digest = kept, ""
	rec.State, rec.Stage, rec.Reason = Pending, StageValidate, "reconciled: the stages that depend on the answers apply again"
	if err := m.save(&rec); err != nil {
		return rec, err
	}
	m.logf("reconciled: %d recorded stage(s) kept", len(kept))
	return rec, nil
}

// bindsAnswers reports whether a stage depends on the answers: every stage from
// verify-host-settings on. prepare-identity observes the instance, whatever the answers say.
func bindsAnswers(stage Stage) bool {
	return slices.Index(Stages, stage) >= slices.Index(Stages, StageHostSettings)
}

// mayHaveStarted reports whether the product's start may have begun. It reads the durable
// field Run sets with Applying@start-services, not the stage, which the next outcome overwrites.
func mayHaveStarted(rec Record) bool {
	_, started := effectOf(rec, StageStartServices)
	return started || rec.ProductMayHaveStarted
}

func effectOf(rec Record, stage Stage) (Effect, bool) {
	for _, c := range rec.Completed {
		if c.Stage == stage {
			return c.Effect, true
		}
	}
	return "", false
}

func lastCompleted(rec Record) Stage {
	if len(rec.Completed) == 0 {
		return StageValidate
	}
	return rec.Completed[len(rec.Completed)-1].Stage
}
