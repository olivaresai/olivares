// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// Bounded reads (BR1) are an OPTIONAL Scope capability. A reader admits the
// representation bound of every variable value before the driver receives it,
// and it charges every statement against explicit caller limits. It never
// falls back to the ordinary unbounded Get/List/Lock paths, and a Scope that
// lacks the capability does not expose it.
var (
	// ErrBoundedReadUnavailable reports that this Scope or engine configuration
	// is not qualified for bounded reads. Ordinary CRUD remains usable.
	ErrBoundedReadUnavailable = errors.New("bounded read unavailable")
	// ErrInvalidBoundedRead reports malformed limits or a malformed request.
	ErrInvalidBoundedRead = errors.New("invalid bounded read")
	// ErrBoundedReadLimit reports that an admission bound does not fit a limit.
	// It names the insufficient bound, never an unobserved actual size.
	ErrBoundedReadLimit = errors.New("bounded read limit")
	// ErrBoundedReadMetadata reports stored metadata that cannot be admitted.
	ErrBoundedReadMetadata = errors.New("bounded read metadata")
	// ErrBoundedReadConsistency reports a row that changed between phases.
	ErrBoundedReadConsistency = errors.New("bounded read consistency")
	// ErrBoundedReadConcurrent reports a call made while another call on the
	// same reader is active. The active call is not disturbed.
	ErrBoundedReadConcurrent = errors.New("bounded read concurrent call")
	// ErrBoundedReadTerminal reports a reader that already failed after SQL
	// started. The original cause remains wrapped.
	ErrBoundedReadTerminal = errors.New("bounded read terminal")
)

// BoundedReaderFactory is the optional Scope capability that creates a bounded
// reader over the Scope's exact transaction and tenant.
type BoundedReaderFactory interface {
	NewBoundedReader(BoundedReadOptions) (BoundedReader, error)
}

// BoundedReadOptions configures one reader. Callers set Limits only. The
// workspace boundary is private and can only be installed by ConfineWorkspace.
type BoundedReadOptions struct {
	Limits   BoundedReadLimits
	boundary *workspaceBoundary
}

// BoundedReadLimits are explicit, positive and immutable for a reader's life.
// There are no defaults: a zero value is rejected.
type BoundedReadLimits struct {
	// MaxCellBytes bounds each admitted variable driver representation.
	MaxCellBytes uint64
	// MaxRowUnits bounds one payload row including all field framing.
	MaxRowUnits uint64
	// MaxPageUnits bounds all statement reservations of one data method.
	MaxPageUnits uint64
	// MaxRowsPerPage bounds payload slots of one data method.
	MaxRowsPerPage uint64
	// MaxRows bounds payload slots across the reader's traversal.
	MaxRows uint64
	// MaxUnits bounds all reservations across the reader's traversal.
	MaxUnits uint64
	// MaxFilters bounds the caller's filters before confinement rewrites them.
	MaxFilters uint64
	// MaxParameterBytes bounds the aggregate request input bytes.
	MaxParameterBytes uint64
	// MaxQueryBytes bounds each generated SQL statement.
	MaxQueryBytes uint64
}

// Validate rejects any zero limit, naming the first missing dimension.
func (l BoundedReadLimits) Validate() error {
	for _, dimension := range []struct {
		name  string
		value uint64
	}{
		{"MaxCellBytes", l.MaxCellBytes},
		{"MaxRowUnits", l.MaxRowUnits},
		{"MaxPageUnits", l.MaxPageUnits},
		{"MaxRowsPerPage", l.MaxRowsPerPage},
		{"MaxRows", l.MaxRows},
		{"MaxUnits", l.MaxUnits},
		{"MaxFilters", l.MaxFilters},
		{"MaxParameterBytes", l.MaxParameterBytes},
		{"MaxQueryBytes", l.MaxQueryBytes},
	} {
		if dimension.value == 0 {
			return fmt.Errorf("%w: %s must be positive", ErrInvalidBoundedRead, dimension.name)
		}
	}
	return nil
}

// PolicyReadAllowed reports whether policy snapshot reads are permitted. A
// workspace-confined reader denies them: policies carry no workspace lineage.
func (o BoundedReadOptions) PolicyReadAllowed() bool { return o.boundary == nil }

// ExtensionConstraint returns the forced lineage filter for a registry-derived
// descriptor. confined=false means no boundary applies. A confined reader over
// a descriptor that declares no lineage receives ErrWorkspaceLineageRequired.
// It opens no repository and executes no SQL.
func (o BoundedReadOptions) ExtensionConstraint(
	desc model.EntityDescriptor,
) (filter model.Filter, confined bool, err error) {
	if o.boundary == nil {
		return model.Filter{}, false, nil
	}
	spec := desc.WorkspaceLineage
	if !spec.Declared() {
		return model.Filter{}, true, denied(string(desc.Kind))
	}
	return o.boundary.filterFor(spec), true, nil
}

// BoundedReader reads complete records within its limits. It is valid only
// for the lifetime of the Scope that created it, and its owner must use that
// Scope sequentially.
type BoundedReader interface {
	GetPolicySnapshot(context.Context, model.ID) (PolicySnapshot, error)
	ListPolicySnapshots(context.Context, model.Query) ([]PolicySnapshot, model.Page, error)
	GetExtension(context.Context, model.Kind, model.ID) (model.Record, error)
	ListExtensions(context.Context, model.Kind, model.Query) ([]model.Record, model.Page, error)
	Usage() BoundedReadUsage
}

// BoundedReadUsage is a content-free counter snapshot. Issued reservations are
// never refunded. ObservedComplete=false means some results were not fully
// observed after a driver failure, so the observed counters are lower bounds.
//
// A wide row may be read through several private statements. Units then
// include every statement's result, row and flag framing. Payload row
// counters count logical rows, never statements: a row's slot is reserved
// once, and it is observed once its final statement has been scanned, even
// if that statement then rejects the row. A row rejected by an earlier
// statement is not counted as observed, and a known rejection is not a
// driver failure, so it leaves ObservedComplete true.
type BoundedReadUsage struct {
	ReservedUnits, ObservedUnits                  uint64
	PayloadRowsReserved, PayloadRowsObserved      uint64
	LookaheadSlotsReserved, LookaheadRowsObserved uint64
	RemainingUnits, RemainingRows                 uint64
	ObservedComplete                              bool
	Terminal                                      bool
}

// workspaceBoundedReaderFactory is the confined bounded-read port. It installs
// its own boundary over whatever the caller passed, so a caller cannot widen
// it, and the reader enforces that boundary before every statement.
type workspaceBoundedReaderFactory struct {
	factory  BoundedReaderFactory
	boundary workspaceBoundary
}

func (p *workspaceBoundedReaderFactory) NewBoundedReader(opts BoundedReadOptions) (BoundedReader, error) {
	boundary := p.boundary
	opts.boundary = &boundary
	return p.factory.NewBoundedReader(opts)
}
