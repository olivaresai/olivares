// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Observation is a fresh owner measurement. Applied and runtime configuration are
// distinct from the stored profile, and neither alone proves persistence.
type Observation struct {
	Profile, Applied                                             Profile
	Managed, SecretsComplete, RuntimeUsable, ManagementUnchanged bool
	ManagementFingerprint                                        string
	ManagementSettingsFingerprint                                string
	RequiresActivation                                           bool
	ManagementDevice                                             bool
	// Runtime is what the device carries now, per family.
	Runtime RuntimeState
}

type Mutation struct {
	restorationVerified   bool
	Method                Method
	Profile               Profile
	Version               uint64
	Flags, TimeoutSeconds uint32
	Checkpoint            string
	EmptySettings         bool
}

// Bus sends to one proven unique owner on one private connection. before must finish
// durably before any message bytes may leave; local cancellation is never a reply.
type Bus interface {
	Observe(context.Context, string) (Observation, error)
	Permissions(context.Context) error
	Send(Mutation, func(CallRecord) error) (<-chan Reply, error)
	OriginalDeath(ProcessIdentity) Death
}
type Clock interface {
	Now() (boot string, boottime time.Duration, err error)
}
type NetworkLock interface {
	Acquire() (release func(), err error)
}
type Probe interface {
	Check(context.Context, Window, Observation, Confirmation) error
}

// Restorer obtains root-helper ledgers, including every earlier attempt. It returns
// pending calls unchanged; a process exit cannot turn one into a completed call.
type RestoreReport struct {
	Calls                       []CallRecord
	Finished, MissingIntentSafe bool
}

// RecoveryProbe measures device-bound reachability for a restored dynamic family.
type RecoveryProbe interface {
	CheckRestoration(context.Context, Window, Observation) ([]Reachability, error)
}

type RecoveryQualifier interface {
	QualifyRecovery(context.Context) (TargetBinding, error)
}

type Restorer interface {
	Restore(context.Context, Window) (RestoreReport, error)
	Report(context.Context, Window) (RestoreReport, error)
}
type Config struct {
	Bus      Bus
	Clock    Clock
	Journal  *Journal
	Lock     NetworkLock
	Probe    Probe
	Restorer Restorer
	CallWait time.Duration
}

type Confirmation struct {
	OperationID string            `json:"operation_id"`
	Digest      string            `json:"digest"`
	Token       string            `json:"token"`
	Class       ConfirmationClass `json:"confirmation_class"`
	// Witness is an opaque probe receipt for the configured verifier, not an authority flag.
	Witness string `json:"probe_witness"`
}

type Status struct {
	ObservedBootID    string        `json:"observed_boot_id"`
	FinalBootID       string        `json:"final_boot_id,omitempty"`
	OperationID       string        `json:"operation_id"`
	State             State         `json:"state"`
	Reason            string        `json:"reason,omitempty"`
	BootID            string        `json:"boot_id"`
	WindowGeneration  string        `json:"window_generation"`
	RecoveryAttempt   uint32        `json:"recovery_attempt"`
	DeadlineRemaining time.Duration `json:"deadline_remaining_ns"`
	ObservedBootTime  time.Duration `json:"observed_boottime_ns"`
	PendingCalls      int           `json:"pending_calls"`
	LastObservation   time.Duration `json:"last_observation_boottime_ns"`
	FinalKnownAt      time.Duration `json:"final_known_boottime_ns,omitempty"`
}

type Engine struct {
	config    Config
	operation sync.Mutex
	windows   map[string]Window
	replies   map[string]<-chan Reply
	view      atomic.Value // immutable map[string]Status, independent of operation or network lock
}

func NewEngine(c Config) (*Engine, error) {
	if c.Bus == nil || c.Clock == nil || c.Journal == nil || c.Lock == nil {
		return nil, errors.New("network_composition_incomplete")
	}
	if c.CallWait <= 0 {
		c.CallWait = CallNotificationAfter
	}
	all, err := c.Journal.All()
	if err != nil {
		return nil, err
	}
	e := &Engine{config: c, windows: map[string]Window{}, replies: map[string]<-chan Reply{}}
	for _, w := range all {
		e.windows[w.OperationID] = cloneWindow(w)
	}
	e.publish()
	return e, nil
}
func (e *Engine) publish() {
	view := make(map[string]Status, len(e.windows))
	boot, now, _ := e.config.Clock.Now()
	for id, w := range e.windows {
		remaining := time.Duration(0)
		if w.BootID == boot && w.Deadline > now {
			remaining = w.Deadline - now
		}
		s := Status{ObservedBootID: boot, FinalBootID: w.FinalBootID, OperationID: id, State: w.State, Reason: w.Reason, BootID: w.BootID, WindowGeneration: w.Generation, RecoveryAttempt: w.Attempt, DeadlineRemaining: remaining, ObservedBootTime: now, LastObservation: w.LastObservation, FinalKnownAt: w.FinalKnownAt}
		for _, c := range w.Calls {
			if !c.Settled {
				s.PendingCalls++
			}
		}
		view[id] = s
	}
	e.view.Store(view)
}
func (e *Engine) Status(id string) (Status, error) {
	if !validID(id) {
		return Status{}, errors.New("network_operation_id_refused")
	}
	s, ok := e.view.Load().(map[string]Status)[id]
	if !ok {
		return Status{}, errors.New("network_operation_unknown")
	}
	return s, nil
}
func (e *Engine) save(w Window) error {
	if err := e.config.Journal.Save(w); err != nil {
		return err
	}
	e.windows[w.OperationID] = cloneWindow(w)
	e.publish()
	return nil
}
func randomHex() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Apply starts one durable window. Repeated ids only retrieve its existing projection;
// a lost plaintext token is not regenerated. A changed digest is refused.
func (e *Engine) Apply(ctx context.Context, change Change) (Status, string, error) {
	e.operation.Lock()
	defer e.operation.Unlock()
	digest, err := change.Digest()
	if err != nil {
		return Status{}, "", err
	}
	if existing, ok := e.windows[change.OperationID]; ok {
		if existing.Digest != digest {
			return Status{}, "", errors.New("network_operation_digest_conflict")
		}
		s, _ := e.Status(change.OperationID)
		return s, "", nil
	}
	for _, w := range e.windows {
		if !w.Terminal() {
			return Status{}, "", errors.New("network_window_open")
		}
	}
	for _, open := range e.windows {
		if !open.Terminal() {
			return Status{}, "", errors.New("network_window_open")
		}
	}
	release, err := e.config.Lock.Acquire()
	if err != nil {
		return Status{}, "", err
	}
	defer release()
	observed, err := e.config.Bus.Observe(ctx, change.Interface)
	if err != nil {
		return Status{}, "", err
	}
	if observed.Profile.UUID != change.ProfileUUID {
		return Status{}, "", errors.New("network_profile_identity_changed")
	}
	if observed.RequiresActivation {
		return Status{}, "", errors.New("activation_not_fenced")
	}
	if !observed.Managed || !observed.Profile.Persistent() || !observed.Profile.Matches(observed.Applied) {
		return Status{}, "", errors.New("network_baseline_unavailable")
	}
	if observed.Profile.ProfileVersion == 0 || observed.Profile.AppliedVersion == 0 {
		return Status{}, "", errors.New("profile_version_unavailable")
	}
	if !observed.SecretsComplete {
		return Status{}, "", errors.New("network_secrets_unavailable")
	}
	if observed.ManagementDevice {
		return Status{}, "", errors.New("network_exposure_dependency_unavailable")
	}
	candidate := observed.Profile.With(change)
	if (!candidate.IPv4.NeverDefault && !sameIP(candidate.IPv4, observed.Profile.IPv4)) || (!candidate.IPv6.NeverDefault && !sameIP(candidate.IPv6, observed.Profile.IPv6)) || !observed.ManagementUnchanged {
		return Status{}, "", errors.New("network_management_path_unproven")
	}
	if err := e.config.Bus.Permissions(ctx); err != nil {
		return Status{}, "", err
	}
	boot, now, err := e.config.Clock.Now()
	if err != nil {
		return Status{}, "", err
	}
	token, err := randomHex()
	if err != nil {
		return Status{}, "", err
	}
	generation, err := randomHex()
	if err != nil {
		return Status{}, "", err
	}
	w := Window{Schema: JournalSchema, OperationID: change.OperationID, Digest: digest, Generation: generation, BootID: boot, State: StatePending, Phase: "create", Class: NonManagement, ManagementFingerprint: observed.ManagementFingerprint, ManagementSettingsFingerprint: observed.ManagementSettingsFingerprint, Baseline: observed.Profile, Candidate: candidate, Deadline: now + time.Duration(change.WindowSeconds)*time.Second, TokenHash: tokenHash(token), AttemptStarted: now}
	if err := e.config.Journal.Create(w); err != nil {
		return Status{}, "", err
	}
	e.windows[w.OperationID] = cloneWindow(w)
	e.publish()
	if err := e.advance(ctx, &w); err != nil {
		return Status{}, "", err
	}
	status, _ := e.Status(w.OperationID)
	return status, token, nil
}

// Confirm consumes its one-use token only after the exact plan-bound probe and fresh
// owner facts pass. Saving and destroying are still nonterminal until measured.
func (e *Engine) Confirm(ctx context.Context, c Confirmation) error {
	e.operation.Lock()
	defer e.operation.Unlock()
	w, ok := e.windows[c.OperationID]
	w = cloneWindow(w)
	if !ok {
		return errors.New("network_operation_unknown")
	}
	if w.Terminal() || w.TokenConsumed || w.State != StateAwaitingConfirmation || w.Pending() {
		return errors.New("network_confirmation_unavailable")
	}
	boot, now, err := e.config.Clock.Now()
	if err != nil {
		return err
	}
	if boot != w.BootID || now >= w.Deadline {
		return errors.New("network_confirmation_expired")
	}
	got := tokenHash(c.Token)
	if c.Digest != w.Digest || c.Class != w.Class || subtle.ConstantTimeCompare([]byte(got), []byte(w.TokenHash)) != 1 {
		return errors.New("network_confirmation_refused")
	}
	release, err := e.config.Lock.Acquire()
	if err != nil {
		return err
	}
	defer release()
	observed, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
	if err != nil {
		return err
	}
	if !candidateObserved(w, observed) {
		return errors.New("network_candidate_unmeasured")
	}
	if e.config.Probe == nil {
		return errors.New("device_probe_required")
	}
	if err := e.config.Probe.Check(ctx, w, observed, c); err != nil {
		return err
	}
	boot, now, err = e.config.Clock.Now()
	if err != nil {
		return err
	}
	if boot != w.BootID || now >= w.Deadline {
		return errors.New("network_confirmation_expired")
	}
	w.TokenConsumed = true
	w.Phase = "save"
	w.State = StatePending
	if err := e.save(w); err != nil {
		return err
	}
	return e.advance(ctx, &w)
}
func candidateObserved(w Window, o Observation) bool {
	return o.ManagementFingerprint == w.ManagementFingerprint && o.Managed && o.Profile.Matches(w.Candidate) && o.Applied.Matches(w.Candidate) && o.RuntimeUsable && o.ManagementUnchanged
}
func baselineObserved(w Window, o Observation) bool {
	return o.ManagementSettingsFingerprint == w.ManagementSettingsFingerprint && o.Managed && o.Profile.Persistent() && o.Profile.Matches(w.Baseline) && o.Applied.Matches(w.Baseline) && o.RuntimeUsable && o.ManagementUnchanged
}

// Tick recovers open windows. It is called by the service independently of clients.
func (e *Engine) Tick(ctx context.Context) error {
	if !e.operation.TryLock() {
		return nil
	}
	defer e.operation.Unlock()
	release, err := e.config.Lock.Acquire()
	if err != nil {
		return err
	}
	defer release()
	for _, initial := range e.windows {
		if initial.Terminal() {
			continue
		}
		w := cloneWindow(initial)
		if err := e.collect(ctx, &w); err != nil {
			return err
		}
		if w.Pending() {
			continue
		}
		boot, now, err := e.config.Clock.Now()
		if err != nil {
			return err
		}
		if boot != w.BootID || now >= w.Deadline {
			if err := e.startRecovery(&w, "deadline_or_reboot"); err != nil {
				return err
			}
		}
		if err := e.advance(ctx, &w); err != nil {
			return err
		}
	}
	e.publish()
	return nil
}
func (e *Engine) Revert(ctx context.Context, id string) error {
	e.operation.Lock()
	defer e.operation.Unlock()
	w, ok := e.windows[id]
	w = cloneWindow(w)
	if !ok {
		return errors.New("network_operation_unknown")
	}
	if w.Terminal() {
		return errors.New("network_operation_terminal")
	}
	release, err := e.config.Lock.Acquire()
	if err != nil {
		return err
	}
	defer release()
	if err := e.startRecovery(&w, "requested"); err != nil {
		return err
	}
	return e.advance(ctx, &w)
}
func (e *Engine) startRecovery(w *Window, reason string) error {
	if w.State == StateRestoring {
		return nil
	}
	w.State = StateRestoring
	w.Phase = "rollback"
	w.Attempt++
	w.Reason = reason
	_, now, err := e.config.Clock.Now()
	if err != nil {
		return err
	}
	w.AttemptStarted = now
	return e.save(*w)
}
func replyKey(c CallRecord) string {
	return c.BusID + "/" + c.ConnectionGeneration + "/" + fmt.Sprint(c.Serial)
}
func (e *Engine) collect(ctx context.Context, w *Window) error {
	if w.HelperStarted {
		if e.config.Restorer == nil {
			return errors.New("network_root_restoration_unavailable")
		}
		report, err := e.config.Restorer.Report(ctx, *w)
		if err != nil {
			return err
		}
		if err := e.mergeRestore(w, report); err != nil {
			return err
		}
	}
	changed := false
	for i := range w.Calls {
		c := &w.Calls[i]
		if c.Settled {
			continue
		}
		var reply Reply
		if ch := e.replies[replyKey(*c)]; ch != nil {
			select {
			case reply = <-ch:
			default:
			}
		}
		death := e.config.Bus.OriginalDeath(c.Target)
		if !c.SettledBy(reply, death) {
			continue
		}
		if err := e.complete(w, i, reply, death); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		return e.save(*w)
	}
	return nil
}
func (e *Engine) complete(w *Window, i int, r Reply, death Death) error {
	c := &w.Calls[i]
	if !c.SettledBy(r, death) {
		return errors.New("network_completion_uncorrelated")
	}
	boot, now, err := e.config.Clock.Now()
	if err != nil {
		return err
	}
	c.EffectBootID = boot
	c.Settled = true
	c.Success = r.Success && r.Completed && r.Sender == c.Target.Unique
	c.EffectKnownAt = now
	c.Settlement = "correlated_reply"
	if death == DeathProven && !r.Completed {
		c.Settlement = "original_process_dead"
		c.Success = false
	}
	if c.Success && c.Method == CheckpointCreate {
		if len(r.Body) != 1 {
			return errors.New("network_checkpoint_reply_invalid")
		}
		w.Checkpoint = fmt.Sprint(r.Body[0])
		if w.Checkpoint == "" || w.Checkpoint == "/" {
			return errors.New("network_checkpoint_reply_invalid")
		}
	}
	if c.Success && c.Method == CheckpointRollback && len(r.Body) == 1 {
		if result, ok := r.Body[0].(map[string]uint32); ok {
			w.RollbackResults = result
		} else {
			return errors.New("network_rollback_reply_invalid")
		}
	}
	delete(e.replies, replyKey(*c))
	return nil
}

// issue returns pending without cancelling the transport. Its last matching call is
// replayed from the journal after a crash, never dispatched a second time.
func (e *Engine) issue(w *Window, m Mutation) (bool, error) {
	if len(w.Calls) > 0 {
		last := w.Calls[len(w.Calls)-1]
		if last.Method == m.Method && last.Attempt == w.Attempt {
			if !last.Settled {
				return false, nil
			}
			if !last.Success {
				return true, errors.New("network_owner_call_failed")
			}
			return true, nil
		}
	}
	if len(w.Calls) >= MaxCallsPerWindow {
		return false, ErrJournalFull
	}
	index := len(w.Calls)
	ch, err := e.config.Bus.Send(m, func(c CallRecord) error {
		if c.Serial == 0 || c.BusID == "" || c.Caller == "" || c.ConnectionGeneration == "" || c.Target.StartTime == 0 {
			return errors.New("network_call_identity_unavailable")
		}
		_, now, err := e.config.Clock.Now()
		if err != nil {
			return err
		}
		c.OperationID = w.OperationID
		c.WindowGeneration = w.Generation
		c.Attempt = w.Attempt
		c.SentAt = now
		w.Calls = append(w.Calls, c)
		return e.save(*w)
	})
	if err != nil {
		return false, err
	}
	if len(w.Calls) != index+1 {
		return false, errors.New("network_transport_omitted_intent")
	}
	e.replies[replyKey(w.Calls[index])] = ch
	timer := time.NewTimer(e.config.CallWait)
	defer timer.Stop()
	select {
	case reply := <-ch:
		if !w.Calls[index].SettledBy(reply, DeathUnknown) {
			return false, nil
		}
		if err := e.complete(w, index, reply, DeathUnknown); err != nil {
			return false, err
		}
		if err := e.save(*w); err != nil {
			return false, err
		}
		if !w.Calls[index].Success {
			return true, errors.New("network_owner_call_failed")
		}
		return true, nil
	case <-timer.C:
		return false, nil
	}
}

func (e *Engine) advance(ctx context.Context, w *Window) error {
	for transitions := 0; transitions < 16; transitions++ {
		if w.Pending() {
			return nil
		}
		boot, now, err := e.config.Clock.Now()
		if err != nil {
			return err
		}
		if (boot != w.BootID || now >= w.Deadline) && w.State != StateRestoring {
			if err := e.startRecovery(w, "deadline_or_reboot"); err != nil {
				return err
			}
		}
		var mutation Mutation
		next := ""
		switch w.Phase {
		case "create":
			mutation = Mutation{Method: CheckpointCreate, Profile: w.Baseline, Flags: 6, TimeoutSeconds: uint32((w.Deadline-w.AttemptStarted)/time.Second) + 30}
			next = "update"
		case "update":
			mutation = Mutation{Method: UpdateInMemory, Profile: w.Candidate, Version: w.Baseline.ProfileVersion, Flags: 2}
			next = "reapply"
		case "reapply":
			o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
			if err != nil {
				return err
			}
			if o.Profile.AppliedVersion == 0 {
				return errors.New("applied_version_unavailable")
			}
			mutation = Mutation{Method: Reapply, Profile: w.Candidate, Version: o.Profile.AppliedVersion, EmptySettings: true}
			next = "observe-candidate"
		case "observe-candidate":
			o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
			if err != nil {
				return err
			}
			if !candidateObserved(*w, o) {
				return e.startRecovery(w, "candidate_postconditions_failed")
			}
			w.State = StateAwaitingConfirmation
			w.Phase = "await"
			w.LastObservation = now
			w.ObservationBootID = boot
			return e.save(*w)
		case "await":
			return nil
		case "save":
			o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
			if err != nil {
				return err
			}
			if !candidateObserved(*w, o) {
				return e.startRecovery(w, "candidate_postconditions_changed")
			}
			mutation = Mutation{Method: UpdateToDisk, Profile: w.Candidate, Version: o.Profile.ProfileVersion, Flags: 1, EmptySettings: true}
			next = "destroy"
		case "destroy":
			o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
			if err != nil {
				return err
			}
			if !candidateObserved(*w, o) || !o.Profile.Persistent() {
				return e.startRecovery(w, "persistence_unmeasured")
			}
			mutation = Mutation{Method: CheckpointDestroy, Profile: w.Candidate, Checkpoint: w.Checkpoint}
			next = "confirmed-observation"
		case "confirmed-observation":
			o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
			if err != nil {
				return err
			}
			if !candidateObserved(*w, o) || !o.Profile.Persistent() {
				return e.startRecovery(w, "confirmation_postconditions_failed")
			}
			boot, now, err = e.config.Clock.Now()
			if err != nil {
				return err
			}
			if boot != w.BootID || now >= w.Deadline {
				if err := e.startRecovery(w, "deadline_or_reboot"); err != nil {
					return err
				}
				continue
			}
			w.State = StateConfirmed
			w.Phase = "final"
			w.LastObservation = now
			w.ObservationBootID = boot
			w.FinalKnownAt = now
			w.FinalBootID = boot
			return e.save(*w)
		case "rollback":
			if w.Checkpoint == "" {
				w.Phase = "restore"
				if err := e.save(*w); err != nil {
					return err
				}
				continue
			}
			mutation = Mutation{Method: CheckpointRollback, Profile: w.Baseline, Checkpoint: w.Checkpoint}
			next = "restore"
		case "restore":
			if !w.HelperStarted {
				o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
				if err == nil && baselineObserved(*w, o) {
					if err := e.recoveryProbe(ctx, *w, o); err != nil {
						return err
					}
					return e.finishRestored(w)
				}
				if e.config.Restorer == nil {
					return errors.New("network_root_restoration_unavailable")
				}
				if len(w.Calls) > MaxCallsPerWindow-2 || len(w.RestoreAttempts) >= MaxCallsPerWindow/2 {
					return ErrJournalFull
				}
				qualifier, ok := e.config.Bus.(RecoveryQualifier)
				if !ok {
					return errors.New("network_restoration_target_unavailable")
				}
				target, err := qualifier.QualifyRecovery(ctx)
				if err != nil {
					return err
				}
				w.RecoveryTarget = target
				w.HelperStarted = true
				w.RestoreAttempts = append(w.RestoreAttempts, RestoreAttemptRecord{Attempt: w.Attempt, Target: target})
				if err := e.save(*w); err != nil {
					return err
				}
			}
			report, err := e.config.Restorer.Restore(ctx, *w)
			if err != nil {
				return err
			}
			if err := e.mergeRestore(w, report); err != nil {
				return err
			}

			if !report.Finished || !report.MissingIntentSafe || w.Pending() {
				return nil
			}
			o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
			if err != nil {
				return e.retryRestoration(w, "network_restoration_owner_unavailable")
			}
			if baselineObserved(*w, o) {
				if err := e.recoveryProbe(ctx, *w, o); err != nil {
					return err
				}
				return e.finishRestored(w)
			}
			if o.RequiresActivation {
				w.Phase = "restore-activate"
				if err := e.save(*w); err != nil {
					return err
				}
				continue
			}
			if !o.SecretsComplete {
				return errors.New("network_secrets_unavailable")
			}
			w.Phase = "restore-memory"
			if err := e.save(*w); err != nil {
				return err
			}
			continue
		case "restore-activate":
			mutation = Mutation{Method: RestoreActivation, Profile: w.Baseline}
			next = "restore-observation"

		case "restore-memory":
			o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
			if err != nil {
				return e.retryRestoration(w, "network_restoration_owner_unavailable")
			}
			mutation = Mutation{Method: UpdateInMemory, Profile: w.Baseline, Version: o.Profile.ProfileVersion, Flags: 2}
			next = "restore-reapply"
		case "restore-reapply":
			o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
			if err != nil {
				return e.retryRestoration(w, "network_restoration_owner_unavailable")
			}
			mutation = Mutation{Method: Reapply, Profile: w.Baseline, Version: o.Profile.AppliedVersion, EmptySettings: true}
			next = "restore-save"
		case "restore-save":
			o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
			if err != nil {
				return e.retryRestoration(w, "network_restoration_owner_unavailable")
			}
			mutation = Mutation{Method: UpdateToDisk, Profile: w.Baseline, Version: o.Profile.ProfileVersion, Flags: 1, EmptySettings: true}
			next = "restore-observation"
		case "restore-observation":
			o, err := e.config.Bus.Observe(ctx, w.Baseline.Interface)
			if err != nil {
				return e.retryRestoration(w, "network_restoration_owner_unavailable")
			}
			if !baselineObserved(*w, o) {
				return e.retryRestoration(w, "network_restoration_unmeasured")
			}
			if err := e.recoveryProbe(ctx, *w, o); err != nil {
				return err
			}
			return e.finishRestored(w)
		case "final":
			return nil
		default:
			return errors.New("network_journal_phase_refused")
		}
		complete, err := e.issue(w, mutation)
		if err != nil {
			if errors.Is(err, ErrJournalFull) || errors.Is(err, os.ErrPermission) {
				return err
			}
			if w.Pending() {
				return nil
			}
			if w.State != StateRestoring {
				return e.startRecovery(w, "network_owner_call_failed")
			}
			if w.Phase == "rollback" {
				w.Phase = "restore"
				if err := e.save(*w); err != nil {
					return err
				}
				continue
			}
			return e.retryRestoration(w, "network_restoration_call_failed")
		}
		if !complete {
			return nil
		}
		w.Phase = next
		if err := e.save(*w); err != nil {
			return err
		}
	}
	return errors.New("network_transition_limit")
}

func (e *Engine) finishRestored(w *Window) error {
	if w.Pending() {
		return errors.New("network_effects_pending")
	}
	boot, now, err := e.config.Clock.Now()
	if err != nil {
		return err
	}
	w.State = StateRolledBack
	w.Phase = "final"
	w.LastObservation = now
	w.ObservationBootID = boot
	w.FinalKnownAt = now
	w.FinalBootID = boot
	return e.save(*w)
}

func (e *Engine) mergeRestore(w *Window, report RestoreReport) error {
	for _, call := range report.Calls {
		valid := false
		for _, attempt := range w.RestoreAttempts {
			if attempt.Attempt == call.Attempt && attempt.Target.BusID == call.BusID && attempt.Target.Process == call.Target {
				valid = true
			}
		}
		if !valid || call.OperationID != w.OperationID || call.WindowGeneration != w.Generation || (call.Method != ReloadConnections && call.Method != LoadConnections) {
			return errors.New("network_restore_receipt_conflict")
		}
		exists := false
		for i, old := range w.Calls {
			if replyKey(old) == replyKey(call) {
				exists = true
				if !sameCall(old, call) {
					return errors.New("network_restore_receipt_conflict")
				}
				if !old.Settled && call.Settled {
					w.Calls[i] = call
				}
			}
		}
		if !exists {
			w.Calls = append(w.Calls, call)
		}
	}
	if len(w.Calls) > MaxCallsPerWindow {
		return ErrJournalFull
	}
	if report.Finished && report.MissingIntentSafe {
		for i := range w.RestoreAttempts {
			w.RestoreAttempts[i].Closed = true
		}
	}
	return e.save(*w)
}

// A settled failure starts another attempt of the same consumed operation. No
// pending call or unfinished root invocation is overtaken, and no record is dropped.
func (e *Engine) retryRestoration(w *Window, reason string) error {
	if w.Pending() {
		return errors.New("network_effects_pending")
	}
	for _, a := range w.RestoreAttempts {
		if !a.Closed {
			return errors.New("network_restore_invocation_unknown")
		}
	}
	if w.Attempt >= MaxCallsPerWindow || len(w.Calls) >= MaxCallsPerWindow {
		return ErrJournalFull
	}
	w.Attempt++
	w.Phase = "restore"
	w.HelperStarted = false
	w.RecoveryTarget = TargetBinding{}
	w.Reason = reason
	_, now, err := e.config.Clock.Now()
	if err != nil {
		return err
	}
	w.AttemptStarted = now
	return e.save(*w)
}

// recoveryProbe applies the dynamic oracle to every DHCP or SLAAC family of the baseline:
// an address, a usable route and a fresh device-bound reachability observation.
func (e *Engine) recoveryProbe(ctx context.Context, w Window, o Observation) error {
	if !dynamicMethod(w.Baseline.IPv4.Method) && !dynamicMethod(w.Baseline.IPv6.Method) {
		return nil
	}
	verifier, ok := e.config.Probe.(RecoveryProbe)
	if !ok {
		return errors.New("network_restoration_probe_required")
	}
	reach, err := verifier.CheckRestoration(ctx, w, o)
	if err != nil {
		return err
	}
	boot, _, err := e.config.Clock.Now()
	if err != nil {
		return err
	}
	for _, f := range []struct {
		want IPSettings
		got  RuntimeFamily
		v4   bool
	}{{w.Baseline.IPv4, o.Runtime.IPv4, true}, {w.Baseline.IPv6, o.Runtime.IPv6, false}} {
		if dynamicMethod(f.want.Method) && !dynamicUsable(w.Baseline.Interface, f.want, f.v4, f.got, reach, boot, w.AttemptStarted) {
			return errors.New("network_restoration_unreachable")
		}
	}
	return nil
}
