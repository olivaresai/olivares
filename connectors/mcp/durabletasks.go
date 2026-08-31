// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	// ErrDurableTaskNotFound means the durable authority has no live binding for
	// the exact owner, task identifier and generation requested. Callers must not
	// fall back to the process-local cache when this error is returned.
	ErrDurableTaskNotFound = errors.New("durable MCP task not found")
	// ErrDurableTaskConflict means Register found that the tenant's task ID is
	// already bound to another live registration. Implementations must detect
	// this atomically; the connector will withhold the new handle and will not
	// issue a wrong-target compensating cancel for the ambiguous identifier.
	ErrDurableTaskConflict = errors.New("durable MCP task conflicts with a live binding")
)

// TaskOwner is the exact, issuer-qualified owner of one MCP task. Tenant-only
// owners are reserved for List during ResourceServer bootstrap; Get and
// UpdateObservation always carry the complete tuple.
type TaskOwner struct {
	Tenant      string
	Issuer      string
	Subject     string
	ActAs       string
	ClientID    string
	IsDelegated bool
}

// complete reports whether the owner can authorize a task operation. It is
// intentionally stricter than the bootstrap List selector.
func (o TaskOwner) complete() bool {
	return strings.TrimSpace(o.Issuer) != "" &&
		strings.TrimSpace(o.Subject) != ""
}

// DurableTaskIntent is the immutable registration input stored before a task
// handle may be returned to an MCP client. It contains identities and hashes,
// never raw tool arguments or task result content.
type DurableTaskIntent struct {
	Owner                TaskOwner
	TaskID               string
	Tool                 string
	RequiredScope        string
	Destructive          bool
	CreatedAt            time.Time
	TTLMs                *int64
	PollIntervalMs       *int64
	InitialStatus        string
	InitialStatusReason  string
	UpstreamDescriptor   string
	ProtocolVersion      string
	OriginOperationID    string
	OriginEffectDigest   string
	InitialInputRequests []DurableTaskInputRef
}

// DurableTaskRef is the durable identity allocated by Register. Generation is
// positive and monotonically distinguishes reuse of the same upstream task ID.
// BindingID, WorkItemID and SID are opaque connector references owned by the
// control-plane adapter.
type DurableTaskRef struct {
	TaskID     string
	Generation int64
	BindingID  string
	WorkItemID string
	SID        string
}

// DurableTaskVerdict is the connector-neutral three-outcome observation
// vocabulary used by the work kernel.
type DurableTaskVerdict string

const (
	DurableTaskVerdictClean        DurableTaskVerdict = "CLEAN"
	DurableTaskVerdictBroken       DurableTaskVerdict = "BROKEN"
	DurableTaskVerdictUnobservable DurableTaskVerdict = "UNKNOWN"
)

// DurableTaskObservationKind names the protocol operation that produced an
// observation. A register observation describes handle registration/relay; get
// is authoritative task status; update and cancel are acknowledgement-only.
type DurableTaskObservationKind string

const (
	DurableTaskObservationRegister DurableTaskObservationKind = "register"
	DurableTaskObservationGet      DurableTaskObservationKind = "get"
	DurableTaskObservationUpdate   DurableTaskObservationKind = "update"
	DurableTaskObservationCancel   DurableTaskObservationKind = "cancel"
)

// DurableTaskObservation is the bounded projection written after an MCP task
// operation. ResultDigest commits to an observed result without persisting its
// content. Status is authoritative only for Kind=get; update/cancel carry the
// acknowledgement and cancellation-intent facts explicitly.
type DurableTaskObservation struct {
	TaskID          string
	Generation      int64
	Kind            DurableTaskObservationKind
	Status          string
	StatusReason    string
	TTLMs           *int64
	PollIntervalMs  *int64
	Verdict         DurableTaskVerdict
	ObservedAt      time.Time
	ResultDigest    string
	OperationID     string
	Dispatched      bool
	Acknowledged    bool
	Terminal        bool
	CancelRequested bool
	InputRequests   []DurableTaskInputRef
}

// DurableTaskInputRef carries only SHA-256 commitments to one exact-cased MCP
// request/response member. KeyDigest commits to the UTF-8 member name and
// ContentDigest to its canonical JSON value; neither raw value crosses this
// persistence boundary.
type DurableTaskInputRef struct {
	KeyDigest     string
	ContentDigest string
}

// DurableTaskInputResponseBatch is the pre-forward durable communication
// command for tasks/update. Responses use the same hash-only representation as
// requests: ContentDigest commits to the canonical response value.
type DurableTaskInputResponseBatch struct {
	TaskID       string
	Generation   int64
	OperationID  string
	EffectDigest string
	Responses    []DurableTaskInputRef
}

// DurableTaskInterruptStore is an additive capability implemented by composed
// stores that can materialize MCP input-required communication locally. A
// ResourceServer with non-empty inputResponses fails closed unless its durable
// task store implements this capability.
type DurableTaskInterruptStore interface {
	PrepareInputResponses(context.Context, TaskOwner, DurableTaskInputResponseBatch) error
}

func durableTaskInputRefs(entries map[string]json.RawMessage) []DurableTaskInputRef {
	if len(entries) == 0 {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	refs := make([]DurableTaskInputRef, 0, len(keys))
	for _, key := range keys {
		keyDigest := sha256.Sum256([]byte(key))
		contentDigest := sha256.Sum256(entries[key])
		refs = append(refs, DurableTaskInputRef{
			KeyDigest:     hex.EncodeToString(keyDigest[:]),
			ContentDigest: hex.EncodeToString(contentDigest[:]),
		})
	}
	return refs
}

// DurableTaskView is the durable source-of-truth projection used to hydrate or
// refresh the in-process task ledger. Intent is immutable; Observation is the
// latest protocol observation for Ref.
type DurableTaskView struct {
	Ref         DurableTaskRef
	Intent      DurableTaskIntent
	Observation DurableTaskObservation
}

// DurableTaskPage is one keyset page of durable task bindings.
type DurableTaskPage struct {
	Tasks      []DurableTaskView
	NextCursor string
}

// DurableTaskStore is the optional, additive persistence port for MCP Tasks.
// The process-local task ledger is only a cache when this port is wired.
//
// Get with generation=0 resolves the owner's current generation. List with a
// TaskOwner containing only Tenant returns the tenant inventory used to
// rehydrate a ResourceServer after restart. Implementations must reject an
// malformed selectors and must keep exact-owner Get/Update calls
// non-enumerating. An empty Tenant remains a valid standalone namespace for
// compatibility; the composed binary supplies a concrete tenant. Register must
// return ErrDurableTaskConflict when the same tenant task ID already belongs to
// another live registration, including a different owner, and must replay the
// same Ref for an identical OriginOperationID+OriginEffectDigest. List returns
// only the current projection for each task ID, not historical generations.
// UpdateObservation is an atomic exact-generation update: it must reject an OCC
// conflict or stale/non-monotonic observation instead of overwriting newer truth.
type DurableTaskStore interface {
	Register(context.Context, DurableTaskIntent) (DurableTaskRef, error)
	Get(context.Context, TaskOwner, string, int64) (DurableTaskView, error)
	UpdateObservation(context.Context, TaskOwner, DurableTaskObservation) error
	List(context.Context, TaskOwner, string, int) (DurableTaskPage, error)
}

func validateDurableTaskRef(ref DurableTaskRef, taskID string) error {
	if err := validateTaskID(ref.TaskID); err != nil {
		return fmt.Errorf("durable task ref: %w", err)
	}
	if ref.TaskID != taskID {
		return fmt.Errorf("durable task ref returned another task identifier")
	}
	if ref.Generation <= 0 {
		return fmt.Errorf("durable task ref generation must be positive")
	}
	if strings.TrimSpace(ref.BindingID) == "" || strings.TrimSpace(ref.WorkItemID) == "" ||
		strings.TrimSpace(ref.SID) == "" {
		return fmt.Errorf("durable task ref is incomplete")
	}
	return nil
}
