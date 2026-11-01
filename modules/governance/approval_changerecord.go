// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// An approval policy may name one record in the change system of record (ServiceNow
// or Jira). Approvals under it hold only while that record is in an approved state:
// the engine reads the record when an approval is decided and again when it is spent,
// and refuses, or revokes, once the record no longer allows the change. Only Business
// authors such a policy and wires a reader; a policy stored without a reader approves
// nothing.

const (
	changeSystemServiceNow = "servicenow"
	changeSystemJira       = "jira"

	maxChangeRecordStates = 16
	maxChangeRecordText   = 64
	// changeRecordTimeout bounds one read; the connector has its own, this is the ceiling
	// over it so a hung system cannot hold an approval open.
	changeRecordTimeout = 10 * time.Second

	codeChangeRecordNotApproved = "change_record_not_approved"
	codeChangeRecordUnavailable = "change_record_unavailable"
)

// defaultChangeStates are the ServiceNow change states that allow work to start. Jira
// workflows are the customer's own, so a Jira policy lists its states.
var defaultChangeStates = map[string][]string{changeSystemServiceNow: {"scheduled", "implement"}}

var (
	changeRecordKey         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	changeRecordDestination = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// changeRecordSpec names the change system, the record in it and the states in which
// the record allows the change. Destination is the notification destination that
// holds the read-only credential by reference; it defaults to the system's name.
type changeRecordSpec struct {
	System      string   `json:"system"`
	Key         string   `json:"key"`
	Destination string   `json:"destination,omitempty"`
	States      []string `json:"states,omitempty"`
}

// canonicalize trims and bounds the spec in place and returns the sentence that
// refuses it, or "".
func (c *changeRecordSpec) canonicalize() string {
	c.System = strings.ToLower(strings.TrimSpace(c.System))
	if c.System != changeSystemServiceNow && c.System != changeSystemJira {
		return "change_record.system must be one of servicenow, jira"
	}
	c.Key = strings.TrimSpace(c.Key)
	if !changeRecordKey.MatchString(c.Key) {
		return "change_record.key must be a record number of up to 64 letters, digits, dots, dashes or underscores, such as CHG0030001"
	}
	c.Destination = strings.TrimSpace(c.Destination)
	if c.Destination != "" && !changeRecordDestination.MatchString(c.Destination) {
		return "change_record.destination must be a destination name of letters, digits, dots, dashes or underscores"
	}
	if len(c.States) > maxChangeRecordStates {
		return "change_record.states lists too many states"
	}
	for i, state := range c.States {
		c.States[i] = strings.ToLower(strings.TrimSpace(state))
		if c.States[i] == "" || len(c.States[i]) > maxChangeRecordText {
			return "change_record.states holds an empty or too long state"
		}
	}
	if len(c.States) == 0 && defaultChangeStates[c.System] == nil {
		return "change_record.states must list the " + c.System + " states in which the change may proceed"
	}
	return ""
}

func (c changeRecordSpec) destination() string {
	if c.Destination != "" {
		return c.Destination
	}
	return c.System
}

func (c changeRecordSpec) allowedStates() []string {
	if len(c.States) > 0 {
		return c.States
	}
	return defaultChangeStates[c.System]
}

// ChangeRecord is what the change system answers for one record: its state in the
// system's own words and, when the system has one, the window the change is approved
// for. A zero Start or End leaves that side of the window open.
type ChangeRecord struct {
	State      string
	Start, End time.Time
}

// ErrChangeRecordNotFound is what a reader answers when the change system replied and
// has no such record: a verdict about the change, not an outage.
var ErrChangeRecordNotFound = errors.New("change record not found")

// ChangeRecordReader reads one record of a change system through the tenant's named
// destination. A Business edition wires it; it holds the read-only credential by
// reference and never returns it.
type ChangeRecordReader func(ctx context.Context, tenant model.TenantID, system, destination, key string) (ChangeRecord, error)

// UseChangeRecordReader wires the reader that checks change-record policies.
func (m *Module) UseChangeRecordReader(r ChangeRecordReader) { m.changeRecords = r }

// changeRecordObservation is what one check saw. It rides the ledger entry of the
// decision, the spend or the revocation: the key, the state and when it was observed.
type changeRecordObservation struct {
	spec       changeRecordSpec
	state      string
	observedAt model.Timestamp
	start      time.Time
	end        time.Time
}

func (o *changeRecordObservation) meta() map[string]any {
	return map[string]any{"system": o.spec.System, "key": o.spec.Key, "state": o.state, "observed_at": o.observedAt.String()}
}

// liveChangeRecord is the change record the policy now governing rec names, or nil.
func liveChangeRecord(pols []model.Policy, rec model.Record) *changeRecordSpec {
	_, spec, matched := matchApprovalSpec(pols, rec.String(colAction), rec.String(colSubjectKind), rec.String(colChangeClass))
	if !matched {
		return nil
	}
	return spec.ChangeRecord
}

// pinnedChangeRecord is the record an approval was decided under, stored on its row when
// it was approved. A policy edited, switched off or deleted afterwards does not change
// what the approval is checked against. A stored pin that cannot be read is an error, not
// an approval without a check.
func pinnedChangeRecord(rec model.Record) (*changeRecordSpec, error) {
	text := rec.String(colChangeRecord)
	if text == "" {
		return nil, nil
	}
	var spec changeRecordSpec
	if err := json.Unmarshal([]byte(text), &spec); err != nil || spec.System == "" || spec.Key == "" {
		return nil, errors.New("the change record this approval was decided under cannot be read")
	}
	return &spec, nil
}

// spendChangeRecord is the record a spend is checked against: the one the approval was
// decided under, else, for an approval no change-record policy covered when it was
// approved, the one the policy now names.
func spendChangeRecord(pols []model.Policy, rec model.Record) (*changeRecordSpec, error) {
	pinned, err := pinnedChangeRecord(rec)
	if err != nil || pinned != nil {
		return pinned, err
	}
	return liveChangeRecord(pols, rec), nil
}

// changeRecordCovers reports whether seen is an observation of exactly the record want
// names and whether its approved window still covers now. The transaction asks it after
// every cheap refusal, so the change system is read only for a request that would succeed.
func changeRecordCovers(want *changeRecordSpec, seen *changeRecordObservation, now model.Timestamp) bool {
	if want == nil || seen == nil {
		return want == nil && seen == nil
	}
	if !reflect.DeepEqual(*want, seen.spec) {
		return false
	}
	return (seen.start.IsZero() || !now.Time().Before(seen.start)) &&
		(seen.end.IsZero() || now.Time().Before(seen.end))
}

// errChangeRecordRead rolls a transaction back to say: read this record, then run it again.
var errChangeRecordRead = errors.New("change record read needed")

// verifyChangeRecord reads the record and answers whether the change may proceed now.
// It returns what it saw whenever it saw anything, so a refusal can be recorded
// (recordChangeRecordRefusal), and whether a refusal is final. A state that does not allow the change and a window that
// has closed are final: the grant is revoked. A record the system says it does not have
// and a window that has not opened are not: the system may be hiding the record from its
// integration user, or the window may still open, so the grant is only refused. Every
// failure to read, and a deployment with no reader, refuses as unavailable: nothing is
// approved on a record that cannot be checked. The connector's own error stays in the
// log; it can name a host.
func (m *Module) verifyChangeRecord(ctx context.Context, tenant model.TenantID, spec changeRecordSpec) (seen *changeRecordObservation, final bool, err error) {
	unavailable := func(why string) error {
		return approvalDecisionError{Status: http.StatusServiceUnavailable, Code: codeChangeRecordUnavailable,
			Message: fmt.Sprintf("Change record %s could not be checked because %s, so nothing is approved under it until it can be.", spec.Key, why)}
	}
	refuse := func(why string) error {
		return approvalDecisionError{Status: http.StatusConflict, Code: codeChangeRecordNotApproved,
			Message: fmt.Sprintf("Change record %s %s.", spec.Key, why)}
	}
	if m.changeRecords == nil {
		m.warnf("governance: a change-record policy cannot be checked, this deployment wires no reader", "system", spec.System)
		return nil, false, unavailable("this deployment cannot read " + spec.System + " change records")
	}
	rctx, cancel := context.WithTimeout(ctx, changeRecordTimeout)
	defer cancel()
	rec, rerr := m.changeRecords(rctx, tenant, spec.System, spec.destination(), spec.Key)
	at := m.clock.Now()
	if errors.Is(rerr, ErrChangeRecordNotFound) {
		return &changeRecordObservation{spec: spec, state: "not found", observedAt: at}, false, refuse("was not found in " + spec.System)
	}
	if rerr != nil {
		m.warnf("governance: change record could not be read", "tenant", tenant.String(), "system", spec.System, "destination", spec.destination(), "key", spec.Key, "err", rerr)
		return nil, false, unavailable("the " + spec.System + " destination did not answer")
	}
	seen = &changeRecordObservation{spec: spec, state: boundedText(rec.State), observedAt: at, start: rec.Start, end: rec.End}
	// The state is compared as the system returned it; the printable part is only what a
	// sentence and the ledger show.
	allowed := spec.allowedStates()
	returned, ok := strings.TrimSpace(rec.State), false
	for _, a := range allowed {
		ok = ok || strings.EqualFold(returned, a)
	}
	switch {
	case !ok:
		return seen, true, refuse(fmt.Sprintf("is %s, and this approval needs it %s", describeState(seen.state), strings.Join(allowed, " or ")))
	case !rec.Start.IsZero() && at.Time().Before(rec.Start):
		return seen, false, refuse("is approved for a window that has not opened yet")
	case !rec.End.IsZero() && !at.Time().Before(rec.End):
		return seen, true, refuse("is approved for a window that has closed")
	}
	return seen, false, nil
}

// boundedText trims what an external system returned to what the ledger and the API
// sentence keep: printable characters only (no control, newline, bidi or zero-width
// characters), on a rune boundary.
func boundedText(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, strings.ToValidUTF8(s, "")))
	for len(s) > maxChangeRecordText {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

func describeState(state string) string {
	if state == "" {
		return "in no state"
	}
	return "in state " + state
}

// revokeForChangeRecord ends an approved, unspent grant whose change record no longer
// allows the change, before anything is issued under it, and records why.
func (m *Module) revokeForChangeRecord(ctx context.Context, data approvalData, mc api.ModuleContext, id model.ID, seen *changeRecordObservation, why error) (ApprovalConsumption, error) {
	now := m.clock.Now()
	resp := ApprovalConsumption{Status: statusCanceled}
	var resolved *approvalDTO
	err := data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		// Only an approved grant nobody has spent is revoked; anything else keeps the
		// answer its own state gives.
		if eff := effectiveStatus(rec, now); eff != statusApproved || rec.String(colConsumedBy) != "" {
			resp.Status = eff
			return nil
		}
		pols, err := loadApprovalPolicies(ctx, sc)
		if err != nil {
			return err
		}
		rec[colStatus], rec[colDecidedAt] = statusCanceled, now.String()
		if rec, err = repo.Update(ctx, rec); err != nil {
			return err
		}
		dto := toApprovalDTO(rec, now, liveRiskTier(pols, rec))
		resolved = &dto
		return auditEvent(ctx, sc, mc, "governance.approval.revoke", approvalKind, id, map[string]any{
			"reason": "change_record", "change_record": seen.meta(), "refusal": why.Error(),
		})
	})
	if err == nil && resolved != nil {
		m.emitApprovalResolved(ctx, mc.Tenant, *resolved)
	}
	return resp, err
}

// recordChangeRecordRefusal writes to the ledger what the change system blocked: a refused
// approval, a refused spend, a refused check. The key is always there; the state and when
// it was observed only when the system answered. The refusal itself stands whether or not
// this entry is written, so a ledger that cannot take it is logged, not returned; the
// result says whether it took it.
func (m *Module) recordChangeRecordRefusal(ctx context.Context, data approvalData, mc api.ModuleContext, id model.ID, step string, spec changeRecordSpec, seen *changeRecordObservation, refusal error) bool {
	record := map[string]any{"system": spec.System, "key": spec.Key}
	if seen != nil {
		record = seen.meta()
	}
	meta := map[string]any{"step": step, "change_record": record}
	var decision approvalDecisionError
	if errors.As(refusal, &decision) {
		meta["code"], meta["refusal"] = decision.Code, decision.Message
	}
	err := data.Mutate(ctx, func(sc store.Scope) error {
		return auditEvent(ctx, sc, mc, "governance.approval.change_record_refused", approvalKind, id, meta)
	})
	if err != nil {
		m.warnf("governance: a change-record refusal could not be written to the ledger", "tenant", mc.Tenant.String(), "approval", id.String(), "step", step, "err", err)
	}
	return err == nil
}

// StillHolds answers whether an approved grant still stands on its change record, for a
// gate that reads an approval's status and never spends it (deploy apply, orchestration,
// voice, the one-shot gates, an MCP call without a round trip). It returns the approval's
// status as it stands after the check: unchanged while the record allows the change,
// "canceled" once it no longer does, when the grant is revoked as at a spend. A record the
// system does not have, a window that has not opened and a system that does not answer
// refuse with an error and revoke nothing. So does a record that no longer allows a grant
// somebody already spent: the spend stands, but the grant is not relied on for anything new.
// An approval that is not approved is answered from its own state without a read. So is an
// approved grant past its own expiry: it answers "expired", as a spend would refuse it,
// whether or not a change record governs it. An approval that no change record governs
// is otherwise answered from its own state.
func (a *EngineApprovals) StillHolds(ctx context.Context, tenant model.TenantID, ref string) (string, error) {
	ctx = api.DetachRequestContext(ctx)
	d, mc, err := a.scope(tenant)
	if err != nil {
		return "", err
	}
	m, id := a.module, model.ID(ref)
	for attempt := 0; attempt < maxDecisionRetries; attempt++ {
		var (
			out       approvalDTO
			want      *changeRecordSpec
			spent     bool
			expiresAt model.Timestamp
			hasExpiry bool
		)
		now := m.clock.Now()
		err := d.View(ctx, func(sc store.Scope) error {
			repo, err := sc.Ext(approvalKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(ctx, id)
			if err != nil {
				return err
			}
			pols, err := loadApprovalPolicies(ctx, sc)
			if err != nil {
				return err
			}
			out = toApprovalDTO(rec, now, liveRiskTier(pols, rec))
			if out.Status != statusApproved {
				return nil
			}
			// A stored-approved grant has the same expiry ceiling here as at a
			// spend. Refuse it from its own state without reading the change system.
			expiresAt, hasExpiry = tsValue(rec, colExpiresAt)
			if hasExpiry && !now.Before(expiresAt) {
				out.Status = statusExpired
				return nil
			}
			spent = rec.String(colConsumedBy) != ""
			if want, err = spendChangeRecord(pols, rec); err != nil {
				return approvalDecisionError{Status: http.StatusServiceUnavailable, Code: codeChangeRecordUnavailable,
					Message: "The change record this approval was decided under cannot be read, so it cannot be relied on."}
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if want == nil {
			return out.Status, nil
		}
		seen, final, verr := m.verifyChangeRecord(ctx, tenant, *want)
		if verr == nil && seen != nil && !seen.end.IsZero() && !m.clock.Now().Time().Before(seen.end) {
			final = true
			verr = approvalDecisionError{Status: http.StatusConflict, Code: codeChangeRecordNotApproved,
				Message: fmt.Sprintf("Change record %s is approved for a window that has closed.", seen.spec.Key)}
		}
		switch {
		case verr == nil:
			if hasExpiry && !m.clock.Now().Before(expiresAt) {
				return statusExpired, nil
			}
			m.checkRefused.Delete(id)
			return statusApproved, nil
		case final && !spent:
			revoked, rerr := m.revokeForChangeRecord(ctx, d, mc, id, seen, verr)
			if isConflict(rerr) {
				continue // a concurrent writer moved the row: look at it again
			}
			if rerr != nil {
				m.checkRefused.Delete(id)
				return "", rerr
			}
			if revoked.Status != statusApproved {
				m.checkRefused.Delete(id)
				return revoked.Status, nil
			}
			// A first spend committed while the record was read: it stands, and the grant
			// is refused below as any spent grant is.
		}
		// A gate polls: the first refusal of a run is a fact for the ledger, its repeats are
		// not, and an answer from the system ends the run. A claim the ledger could not
		// take is given back, so the next poll offers that refusal to it again.
		if last, _ := m.checkRefused.Swap(id, verr.Error()); last != verr.Error() {
			if !m.recordChangeRecordRefusal(ctx, d, mc, id, "check", *want, seen, verr) {
				m.checkRefused.CompareAndDelete(id, verr.Error())
			}
		}
		return "", verr
	}
	return "", store.ErrConflict
}
