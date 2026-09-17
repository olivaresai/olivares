// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// StreamRecorder is the neutral seam for recording a long-lived, bidirectional STREAM
// of bytes — a governed terminal's input and output (V269 /
// docs/contracts/COCKPIT-05-recording-ledger.md §2).
//
// ⛔ IT IS DELIBERATELY NOT SessionRecorder, AND WIDENING THAT ONE INSTEAD WOULD BREAK A
// PROPERTY THE WHOLE PRODUCT RELIES ON. SessionRecorder's own contract says it
// "deliberately carries no request body and no query VALUES: the recording layer is
// minimal-data by construction — route shape, identifiers and a one-way body digest,
// nothing a secret could ride in" (recording.go). Terminal bytes ARE the content, so
// passing them through that seam would retire minimal-data for EVERY route in the
// engine, not just for the cockpit's.
//
// So there are two seams with two shapes, injected from the same recording module by
// composition: one CLASSIFIES a call and keeps a digest, this one CUSTODIES a flow.
//
// ⛔ AND IT IS NOT A SECOND GATE. The deny-closed admission decision stays exactly where
// it is — SessionRecorder.Gate, before the handler. A cockpit route is admitted by that
// gate like every other module route; Reserve here is a CAPACITY reservation for the
// stream that follows, not a second yes/no about whether the caller may act.
//
// The engine defines only the seam. Nothing in core implements it, and the open build
// wires none: a deployment without a stream recorder simply has no route that reserves.
type StreamRecorder interface {
	// Reserve claims durable capacity for one stream BEFORE it opens.
	//
	// It is a precondition, not an afterthought: a governed input session may not
	// reach ACTIVE until this succeeds, because evidence that cannot be written is
	// evidence that will not exist for the bytes about to flow. An error denies the
	// opening — there is no "record if you can" mode.
	Reserve(ctx context.Context, req StreamReservation) (StreamReservationID, error)
	// AppendFrame persists one frame. For the INPUT direction the caller must not
	// deliver the bytes onward until this returns nil: a byte delivered and not
	// recorded is evidence lost for good, while a byte recorded and not delivered is
	// a frame that reconciles from a receipt. The asymmetry is the whole ordering.
	AppendFrame(ctx context.Context, id StreamReservationID, frame StreamFrame) error
	// Close seals the stream with a typed reason and releases the reservation. It is
	// idempotent: a stream may be closed by the operator, by a TTL, by a revocation
	// and by a lost connection, and more than one of those can arrive.
	Close(ctx context.Context, id StreamReservationID, reasonCode string) error
}

// StreamReservationID identifies one reserved stream.
type StreamReservationID string

// IsZero reports whether the reservation is unset.
func (id StreamReservationID) IsZero() bool { return id == "" }

// StreamReservation is what the caller declares when reserving.
//
// It carries IDENTIFIERS and a classification — never content, and never a retention
// duration chosen here: the retention POLICY is resolved by the recorder from the
// classification and the tenant, so a caller cannot ask for a shorter one.
type StreamReservation struct {
	// Tenant owning the stream.
	Tenant model.TenantID
	// CoreSessionID is the authorization row (COCKPIT-01: never the SID).
	CoreSessionID model.ID
	// SessionSID is the SG-00 work identity, carried alongside and never instead.
	SessionSID string
	// Classification is the resolved sensitivity level of the target. The recorder
	// picks storage class, key scope and retention from it.
	Classification string
	// Actor is the principal opening the stream.
	Actor model.ID
	// AAL is the actor's assurance level at the moment of opening.
	AAL int
	// NodeID is the host the stream terminates on, from its verified certificate.
	NodeID string
	// PaneGeneration is the exact target generation, so a reservation cannot be
	// reused across a pane that was replaced underneath it.
	PaneGeneration string
}

// StreamDirection distinguishes the two halves of a recorded stream. They share one
// sequence space so the transcript reads as one interleaved conversation rather than
// two files a reader has to align.
type StreamDirection uint8

const (
	// StreamDirectionUnspecified is the zero value and is never valid on a frame.
	StreamDirectionUnspecified StreamDirection = iota
	// StreamDirectionInput is what the operator sent toward the process.
	StreamDirectionInput
	// StreamDirectionOutput is what the process produced.
	StreamDirectionOutput
)

// String renders the direction for evidence.
func (d StreamDirection) String() string {
	switch d {
	case StreamDirectionInput:
		return "input"
	case StreamDirectionOutput:
		return "output"
	default:
		return "unspecified"
	}
}

// StreamFrame is one recorded chunk.
type StreamFrame struct {
	// Direction is input or output. The zero value is invalid on purpose: a frame
	// whose direction defaulted would be attributed to the wrong half of the
	// transcript, and silently.
	Direction StreamDirection
	// Seq is monotonic within the stream, ACROSS both directions.
	Seq uint64
	// MonotonicAt is the offset from the stream's start. It is a monotonic offset
	// and not a wall clock because the transcript's ORDER must not depend on a host
	// clock that can step.
	MonotonicAt time.Duration
	// Bytes is the frame's exact content.
	Bytes []byte
	// Digest is the continuity digest the producer computed. The recorder seals
	// segments with its OWN key; this is transport continuity, never the seal — the
	// distinction is COCKPIT-05 §3 and it is what keeps the seal meaningful against
	// the party that produced the bytes.
	Digest []byte
}
