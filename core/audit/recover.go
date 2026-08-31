// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const recoverDomain = "olivares.audit.recover.v1"

// RecoveryEvidence is the complete, non-secret evidence sealed into an
// audit.recover epoch-boundary marker. The marker does not repair, move or
// delete any event: it permanently documents which tail is quarantined and
// which earlier off-box checkpoint remains the last trusted anchor.
type RecoveryEvidence struct {
	Tenant              string   `json:"tenant"`
	BreakReason         string   `json:"break_reason"`
	BreakAt             int64    `json:"break_at"`
	ReanchorSeq         int64    `json:"reanchor_seq"`
	OffBoxCheckpointSeq int64    `json:"offbox_checkpoint_seq"`
	OffBoxKeyID         string   `json:"offbox_key_id"`
	QuarantinedFrom     int64    `json:"quarantined_from"`
	QuarantinedTo       int64    `json:"quarantined_to"`
	QuarantinedSHA256   string   `json:"quarantined_sha256"`
	Approvers           []string `json:"approvers"`
	Reason              string   `json:"reason"`
	RequestedBy         string   `json:"requested_by"`
}

// RecoveryMarkerReport is the outcome of verifying every audit.recover event
// in a ledger. An absent marker is neutral, but once the reserved action is
// present it must carry a valid pinned off-box signature and occupy the exact
// sequence immediately after the tail its signed evidence quarantines.
type RecoveryMarkerReport struct {
	Markers     int    `json:"markers"`
	Valid       int    `json:"valid"`
	OK          bool   `json:"ok"`
	FirstBadSeq int64  `json:"first_bad_seq,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// RecordRecovery appends one signed fork-forward marker. Only the configured
// off-box checkpoint key may sign it; the on-box audit key is deliberately not
// a fallback because the marker is the custody boundary after a host/DB
// integrity incident.
func RecordRecovery(ctx context.Context, log store.AuditLog, signer *Signer, ev RecoveryEvidence) (model.AuditEvent, error) {
	if log == nil {
		return model.AuditEvent{}, fmt.Errorf("audit: recovery: nil audit log")
	}
	if signer == nil || !signer.OffBoxCheckpoints() || signer.CheckpointKey() == nil {
		return model.AuditEvent{}, fmt.Errorf("audit: recovery requires an off-box checkpoint signer")
	}

	keyID := strings.TrimSpace(signer.CheckpointKey().KeyID())
	if ev.OffBoxKeyID == "" {
		ev.OffBoxKeyID = keyID
	} else if ev.OffBoxKeyID != keyID {
		return model.AuditEvent{}, fmt.Errorf("audit: recovery off-box key id %q does not match signer %q", ev.OffBoxKeyID, keyID)
	}
	if err := validateRecoveryEvidence(ev); err != nil {
		return model.AuditEvent{}, err
	}

	sig, sigMeta, err := signer.signCheckpoint(ctx, recoverPreimage(ev))
	if err != nil {
		return model.AuditEvent{}, fmt.Errorf("audit: recovery sign: %w", err)
	}
	meta := recoveryMeta(ev)
	for k, value := range sigMeta {
		meta[k] = value
	}
	marker, err := log.Append(ctx, model.AuditDraft{
		Actor:      model.ActorSystem,
		ActorKind:  model.ActorSystem,
		Action:     store.ActionAuditRecover,
		TargetKind: "core.audit_recovery",
		Meta:       meta,
		Sig:        sig,
	})
	if err != nil {
		return model.AuditEvent{}, err
	}
	// Append assigns the sequence only after the off-box signature exists. A
	// concurrent Postgres writer may have advanced the tail since the caller
	// derived ev.QuarantinedTo; never allow that displaced, permanently invalid
	// marker to commit. Returning an error from the surrounding Mutate rolls the
	// INSERT back, leaving a clean retry instead of a durable fail-closed DoS.
	if marker.Seq < 1 || marker.Seq-1 != ev.QuarantinedTo {
		return model.AuditEvent{}, fmt.Errorf("audit: recovery marker landed at seq %d but signed evidence quarantines up to %d; a concurrent append shifted the tail — retry the recovery", marker.Seq, ev.QuarantinedTo)
	}
	return marker, nil
}

// LocateRecovery returns the latest valid off-box-signed recovery marker. An
// absent/empty verifier never honors a marker: callers must pin the public key
// independently from the possibly compromised engine and database.
func LocateRecovery(ctx context.Context, log store.AuditLog, v *CheckpointVerifier) (found bool, recoverSeq, reanchorSeq int64, approvers []string, err error) {
	found, recoverSeq, evidence, err := LocateRecoveryEvidence(ctx, log, v)
	if err != nil || !found {
		return found, recoverSeq, 0, nil, err
	}
	return true, recoverSeq, evidence.ReanchorSeq, append([]string(nil), evidence.Approvers...), nil
}

// LocateRecoveryEvidence is the detailed form used by the command layer to
// prove that a marker documents the exact genesis-walk break it is reporting.
// LocateRecovery remains the stable compact seam for other consumers.
func LocateRecoveryEvidence(ctx context.Context, log store.AuditLog, v *CheckpointVerifier) (found bool, recoverSeq int64, evidence RecoveryEvidence, err error) {
	if log == nil || v == nil || v.Empty() {
		return false, 0, RecoveryEvidence{}, nil
	}
	err = walkRecoveryEvents(ctx, log, func(event model.AuditEvent, metaJSON []byte) error {
		if event.Action != store.ActionAuditRecover || len(event.Sig) == 0 {
			return nil
		}
		candidate, reason := verifyRecoveryMarker(event, metaJSON, v)
		if reason != "" {
			return nil
		}
		if !found || event.Seq > recoverSeq {
			found = true
			recoverSeq = event.Seq
			evidence = candidate
		}
		return nil
	})
	if err != nil {
		return false, 0, RecoveryEvidence{}, err
	}
	return found, recoverSeq, evidence, nil
}

// VerifyRecoveryMarkersWith verifies every reserved recovery marker. Unlike
// LocateRecoveryEvidence, which deliberately ignores invalid candidates while
// locating the latest honorable boundary, this pass is fail-closed: one bad
// marker makes the whole report fail. Callers must run it even when verifying a
// later epoch so a forged marker elsewhere in the immutable ledger cannot hide.
func VerifyRecoveryMarkersWith(ctx context.Context, log store.AuditLog, v *CheckpointVerifier) (RecoveryMarkerReport, error) {
	// OK starts false and is flipped true only behind the completed evidence walk
	// (no marker recorded a failure) — an assurance verifier never asserts a literal
	// success. Absence of markers is neutral, so an empty ledger still verifies OK.
	rep := RecoveryMarkerReport{}
	if log == nil {
		return RecoveryMarkerReport{}, fmt.Errorf("audit: nil audit log")
	}
	if v == nil || v.Empty() {
		return RecoveryMarkerReport{}, fmt.Errorf("audit: no recovery verification key configured")
	}
	err := walkRecoveryEvents(ctx, log, func(event model.AuditEvent, metaJSON []byte) error {
		if event.Action != store.ActionAuditRecover {
			return nil
		}
		rep.Markers++
		_, reason := verifyRecoveryMarker(event, metaJSON, v)
		if reason != "" {
			rep.fail(event.Seq, reason)
			return nil
		}
		rep.Valid++
		return nil
	})
	if err != nil {
		return RecoveryMarkerReport{}, err
	}
	if rep.Reason == "" {
		rep.OK = true
	}
	return rep, nil
}

func (r *RecoveryMarkerReport) fail(seq int64, reason string) {
	r.OK = false
	if r.FirstBadSeq == 0 {
		r.FirstBadSeq = seq
		r.Reason = reason
	}
}

func verifyRecoveryMarker(event model.AuditEvent, metaJSON []byte, v *CheckpointVerifier) (RecoveryEvidence, string) {
	candidate, err := decodeRecoveryEvidence(metaJSON)
	if err != nil || validateRecoveryEvidence(candidate) != nil || candidate.Tenant != event.TenantID.String() ||
		len(event.Sig) == 0 || !v.verify(recoverPreimage(candidate), event.Sig) {
		return RecoveryEvidence{}, "recovery-sig-invalid"
	}
	// Use subtraction rather than candidate.QuarantinedTo+1 so malicious
	// MaxInt64 metadata cannot wrap while checking the same signed invariant.
	if event.Seq < 1 || candidate.QuarantinedTo != event.Seq-1 {
		return RecoveryEvidence{}, "recovery-position-invalid"
	}
	return candidate, ""
}

// recoverPreimage is a versioned, domain-separated, length-prefixed encoding of
// every evidence field. It intentionally excludes the marker's assigned seq:
// Append chooses that seq atomically after the off-box signer has produced the
// signature, exactly as Checkpoint signs the prior head before appending.
func recoverPreimage(ev RecoveryEvidence) []byte {
	var buf []byte
	buf = lenPrefix(buf, []byte(recoverDomain))
	buf = lenPrefix(buf, []byte(ev.Tenant))
	buf = lenPrefix(buf, []byte(ev.BreakReason))
	buf = appendRecoveryInt64(buf, ev.BreakAt)
	buf = appendRecoveryInt64(buf, ev.ReanchorSeq)
	buf = appendRecoveryInt64(buf, ev.OffBoxCheckpointSeq)
	buf = lenPrefix(buf, []byte(ev.OffBoxKeyID))
	buf = appendRecoveryInt64(buf, ev.QuarantinedFrom)
	buf = appendRecoveryInt64(buf, ev.QuarantinedTo)
	buf = lenPrefix(buf, []byte(ev.QuarantinedSHA256))
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(ev.Approvers)))
	buf = append(buf, count[:]...)
	for _, approver := range ev.Approvers {
		buf = lenPrefix(buf, []byte(approver))
	}
	buf = lenPrefix(buf, []byte(ev.Reason))
	return lenPrefix(buf, []byte(ev.RequestedBy))
}

func appendRecoveryInt64(dst []byte, value int64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(value))
	return append(dst, raw[:]...)
}

func recoveryMeta(ev RecoveryEvidence) map[string]any {
	return map[string]any{
		"tenant":                ev.Tenant,
		"break_reason":          ev.BreakReason,
		"break_at":              ev.BreakAt,
		"reanchor_seq":          ev.ReanchorSeq,
		"offbox_checkpoint_seq": ev.OffBoxCheckpointSeq,
		"offbox_key_id":         ev.OffBoxKeyID,
		"quarantined_from":      ev.QuarantinedFrom,
		"quarantined_to":        ev.QuarantinedTo,
		"quarantined_sha256":    ev.QuarantinedSHA256,
		"approvers":             append([]string(nil), ev.Approvers...),
		"reason":                ev.Reason,
		"requested_by":          ev.RequestedBy,
	}
}

func validateRecoveryEvidence(ev RecoveryEvidence) error {
	switch {
	case strings.TrimSpace(ev.Tenant) == "":
		return fmt.Errorf("audit: recovery evidence tenant is required")
	case strings.TrimSpace(ev.BreakReason) == "":
		return fmt.Errorf("audit: recovery evidence break reason is required")
	case ev.BreakAt < 1:
		return fmt.Errorf("audit: recovery evidence break_at must be positive")
	case ev.ReanchorSeq < 1 || ev.ReanchorSeq >= ev.BreakAt:
		return fmt.Errorf("audit: recovery evidence reanchor_seq must precede break_at")
	case ev.OffBoxCheckpointSeq != ev.ReanchorSeq:
		return fmt.Errorf("audit: recovery evidence off-box checkpoint must equal reanchor_seq")
	case strings.TrimSpace(ev.OffBoxKeyID) == "":
		return fmt.Errorf("audit: recovery evidence off-box key id is required")
	case ev.QuarantinedFrom != ev.BreakAt:
		return fmt.Errorf("audit: recovery evidence quarantined_from must equal break_at")
	case ev.QuarantinedTo < ev.QuarantinedFrom:
		return fmt.Errorf("audit: recovery evidence quarantine range is invalid")
	}
	digest, err := hex.DecodeString(ev.QuarantinedSHA256)
	if err != nil || len(digest) != 32 {
		return fmt.Errorf("audit: recovery evidence quarantined_sha256 must be a SHA-256 hex digest")
	}
	seen := make(map[string]struct{}, len(ev.Approvers))
	for _, principal := range ev.Approvers {
		principal = strings.TrimSpace(principal)
		if principal != "" {
			seen[principal] = struct{}{}
		}
	}
	if len(seen) < 2 {
		return fmt.Errorf("audit: recovery evidence requires two distinct approvers")
	}
	return nil
}

func decodeRecoveryEvidence(raw []byte) (RecoveryEvidence, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var ev RecoveryEvidence
	if err := dec.Decode(&ev); err != nil {
		return RecoveryEvidence{}, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RecoveryEvidence{}, fmt.Errorf("audit: recovery metadata has trailing data")
	}
	return ev, nil
}

func walkRecoveryEvents(ctx context.Context, log store.AuditLog, fn func(model.AuditEvent, []byte) error) error {
	if canonical, ok := log.(store.CanonicalWalker); ok {
		return canonical.WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
			return fn(ev, []byte(meta))
		})
	}
	return log.Walk(ctx, 1, func(ev model.AuditEvent) error {
		raw, err := json.Marshal(ev.Meta)
		if err != nil {
			return err
		}
		return fn(ev, raw)
	})
}
