// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Directory entity kinds are constants because the durable evidence rows and
// the read witness must agree on their exact spelling.
const (
	DirectoryEpochKind     Kind = "core.directory_epoch"
	DirectoryTombstoneKind Kind = "core.directory_tombstone"
	UserTombstoneKind      Kind = "core.user_tombstone"
)

// ErrInvalidDirectoryEvidence marks a malformed epoch or tombstone. A reader
// must treat it as unavailable evidence, never as proof that a principal was
// retired or that an epoch is zero.
var ErrInvalidDirectoryEvidence = errors.New("invalid directory evidence")

// DirectoryPrincipalKind is the closed set of principals for which core can
// persist irreversible-retirement evidence. Sessions deliberately have no
// retirement tombstone in K3.
type DirectoryPrincipalKind string

const (
	DirectoryPrincipalUser     DirectoryPrincipalKind = "user"
	DirectoryPrincipalIdentity DirectoryPrincipalKind = "identity"
	DirectoryPrincipalAgent    DirectoryPrincipalKind = "agent"
)

// Valid reports whether k is a known directory principal kind.
func (k DirectoryPrincipalKind) Valid() bool {
	switch k {
	case DirectoryPrincipalUser, DirectoryPrincipalIdentity, DirectoryPrincipalAgent:
		return true
	default:
		return false
	}
}

// DirectoryRetirementCause is intentionally closed. Reversible deactivation,
// deprovisioning, membership loss and provider failures are not members and
// therefore cannot be persisted as definitive retirement.
type DirectoryRetirementCause string

const (
	DirectoryCauseUserErased      DirectoryRetirementCause = "user_erased"
	DirectoryCauseIdentityRetired DirectoryRetirementCause = "identity_retired"
	DirectoryCauseAgentRetired    DirectoryRetirementCause = "agent_retired"
)

// Valid reports whether c is one of the three definitive causes K3 accepts.
func (c DirectoryRetirementCause) Valid() bool {
	switch c {
	case DirectoryCauseUserErased, DirectoryCauseIdentityRetired, DirectoryCauseAgentRetired:
		return true
	default:
		return false
	}
}

// DirectoryEpoch is the single tenant-local fencing fact. Its BaseFields.ID
// and TenantID are the same real UUIDv7 and BaseFields.Version is the epoch.
// SystemTenantID never has a row.
type DirectoryEpoch struct {
	BaseFields
}

// Validate verifies the complete durable epoch shape.
func (e DirectoryEpoch) Validate() error {
	if err := validateDirectoryTenant(e.TenantID); err != nil {
		return err
	}
	if err := validateDirectoryID(e.ID, "epoch id"); err != nil {
		return err
	}
	if e.ID.String() != e.TenantID.String() {
		return fmt.Errorf("%w: epoch id must equal tenant id", ErrInvalidDirectoryEvidence)
	}
	if e.Version < 1 {
		return fmt.Errorf("%w: epoch version must be at least one", ErrInvalidDirectoryEvidence)
	}
	return nil
}

// TenantEpoch is one entry in the immutable user-retirement map. Entries are
// held in strictly increasing canonical tenant-id order; the SQL codec stores
// them as one canonical JSON object {tenant_id: resulting_epoch}.
type TenantEpoch struct {
	TenantID TenantID
	Epoch    int64
}

// DirectoryEpochEvidence is the ordered in-memory form of a User tombstone's
// global tenant-to-resulting-epoch map. Callers must construct it through
// NewDirectoryEpochEvidence or preserve its strict ordering.
type DirectoryEpochEvidence []TenantEpoch

// NewDirectoryEpochEvidence copies a tenant->epoch map into canonical order.
func NewDirectoryEpochEvidence(in map[TenantID]int64) (DirectoryEpochEvidence, error) {
	out := make(DirectoryEpochEvidence, 0, len(in))
	for tenant, epoch := range in {
		out = append(out, TenantEpoch{TenantID: tenant, Epoch: epoch})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].TenantID.String() < out[j].TenantID.String()
	})
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// Validate verifies real UUIDv7 tenants, positive epochs, uniqueness and
// canonical byte order.
func (e DirectoryEpochEvidence) Validate() error {
	for i, entry := range e {
		if err := validateDirectoryTenant(entry.TenantID); err != nil {
			return fmt.Errorf("epoch evidence entry %d: %w", i, err)
		}
		if entry.Epoch < 1 {
			return fmt.Errorf("%w: epoch evidence entry %d is below one",
				ErrInvalidDirectoryEvidence, i)
		}
		if i > 0 && e[i-1].TenantID.String() >= entry.TenantID.String() {
			return fmt.Errorf("%w: epoch evidence is not strictly ordered",
				ErrInvalidDirectoryEvidence)
		}
	}
	return nil
}

// EpochFor returns the resulting epoch recorded for tenant. The bool is false
// when this global tombstone carries no evidence for that tenant.
func (e DirectoryEpochEvidence) EpochFor(tenant TenantID) (int64, bool) {
	i := sort.Search(len(e), func(i int) bool {
		return e[i].TenantID.String() >= tenant.String()
	})
	if i == len(e) || e[i].TenantID != tenant {
		return 0, false
	}
	return e[i].Epoch, true
}

// RetirementAuditAnchor binds a tombstone to the exact audit event appended in
// the same transaction. Hash is the 32-byte audit chain hash; EventID and Seq
// make the anchor independently addressable and revalidatable.
type RetirementAuditAnchor struct {
	EventID    ID
	Seq        int64
	Hash       []byte
	Action     string
	TargetKind Kind
	TargetID   ID
}

const (
	// AuditActionUserRetire is the sole audit action accepted by a User tombstone.
	AuditActionUserRetire = "user.retire"
	// AuditActionDirectoryPrincipalRetire is the sole action accepted by an
	// Identity or Agent tombstone.
	AuditActionDirectoryPrincipalRetire = "directory_principal.retire"
	// AuditActionAgentBindingRetire records physical retirement of one Agent
	// binding when another physically recoverable binding for the same stable
	// principal/workspace remains. It deliberately cannot anchor a tombstone:
	// the recipient has not been shown definitively retired.
	AuditActionAgentBindingRetire = "agent.binding.retire"
)

// Validate verifies an audit anchor against its expected tombstone target.
func (a RetirementAuditAnchor) Validate(action string, kind Kind, id ID) error {
	if err := validateDirectoryID(a.EventID, "audit event id"); err != nil {
		return err
	}
	if a.Seq < 1 {
		return fmt.Errorf("%w: audit sequence must be positive", ErrInvalidDirectoryEvidence)
	}
	if len(a.Hash) != 32 {
		return fmt.Errorf("%w: audit hash must be 32 bytes", ErrInvalidDirectoryEvidence)
	}
	if a.Action != action {
		return fmt.Errorf("%w: audit action %q does not match %q",
			ErrInvalidDirectoryEvidence, a.Action, action)
	}
	if a.TargetKind != kind || a.TargetID != id {
		return fmt.Errorf("%w: audit target does not match tombstone",
			ErrInvalidDirectoryEvidence)
	}
	return nil
}

// UserTombstone is irreversible evidence for a globally stored User. The row
// itself belongs to SystemTenantID; ResultingEpochs preserves one independently
// fenced epoch for every affected real tenant.
type UserTombstone struct {
	BaseFields
	PrincipalKind   DirectoryPrincipalKind
	PrincipalRef    ID
	SourceKind      Kind
	SourceID        ID
	ResultingEpochs DirectoryEpochEvidence
	Cause           DirectoryRetirementCause
	Actor           string
	RetiredAt       Timestamp
	AuditAnchor     RetirementAuditAnchor
}

// Validate verifies a complete User tombstone without inferring evidence from
// absence or from a free-form cause string.
func (t UserTombstone) Validate() error {
	if err := validateDirectoryID(t.ID, "user tombstone id"); err != nil {
		return err
	}
	if t.Version != 1 {
		return fmt.Errorf("%w: user tombstone version must be one",
			ErrInvalidDirectoryEvidence)
	}
	if t.TenantID != SystemTenantID {
		return fmt.Errorf("%w: user tombstone must belong to system tenant",
			ErrInvalidDirectoryEvidence)
	}
	if t.PrincipalKind != DirectoryPrincipalUser {
		return fmt.Errorf("%w: user tombstone principal kind must be user",
			ErrInvalidDirectoryEvidence)
	}
	if err := validateDirectoryID(t.PrincipalRef, "user principal ref"); err != nil {
		return err
	}
	if t.SourceKind != Kind("core.user") {
		return fmt.Errorf("%w: user tombstone source kind must be core.user",
			ErrInvalidDirectoryEvidence)
	}
	if err := validateDirectoryID(t.SourceID, "user source id"); err != nil {
		return err
	}
	if t.SourceID != t.PrincipalRef {
		return fmt.Errorf("%w: user source id must equal principal ref",
			ErrInvalidDirectoryEvidence)
	}
	if t.Cause != DirectoryCauseUserErased {
		return fmt.Errorf("%w: user tombstone cause must be user_erased",
			ErrInvalidDirectoryEvidence)
	}
	if err := t.ResultingEpochs.Validate(); err != nil {
		return err
	}
	if err := validateRetirementCommon(t.Actor, t.RetiredAt); err != nil {
		return err
	}
	return t.AuditAnchor.Validate(AuditActionUserRetire, UserTombstoneKind, t.ID)
}

// DirectoryTombstone is irreversible tenant-local evidence for one Identity or
// Agent recipient. WorkspaceRef uses the Go zero value when workspace does not
// participate in that recipient's identity. The SQL codec maps that one public
// spelling to its canonical non-NULL nil-UUID storage sentinel.
type DirectoryTombstone struct {
	BaseFields
	PrincipalKind  DirectoryPrincipalKind
	PrincipalRef   ID
	SourceKind     Kind
	SourceID       ID
	WorkspaceRef   ID
	ResultingEpoch int64
	Cause          DirectoryRetirementCause
	Actor          string
	RetiredAt      Timestamp
	AuditAnchor    RetirementAuditAnchor
}

// Validate verifies a complete tenant-local tombstone and its audit/epoch
// evidence.
func (t DirectoryTombstone) Validate() error {
	if err := validateDirectoryID(t.ID, "directory tombstone id"); err != nil {
		return err
	}
	if t.Version != 1 {
		return fmt.Errorf("%w: directory tombstone version must be one",
			ErrInvalidDirectoryEvidence)
	}
	if err := validateDirectoryTenant(t.TenantID); err != nil {
		return err
	}
	if t.PrincipalKind != DirectoryPrincipalIdentity &&
		t.PrincipalKind != DirectoryPrincipalAgent {
		return fmt.Errorf("%w: directory tombstone principal must be identity or agent",
			ErrInvalidDirectoryEvidence)
	}
	if err := validateDirectoryID(t.PrincipalRef, "directory principal ref"); err != nil {
		return err
	}
	wantSource := Kind("core.identity")
	wantCause := DirectoryCauseIdentityRetired
	if t.PrincipalKind == DirectoryPrincipalAgent {
		wantSource = "core.agent"
		wantCause = DirectoryCauseAgentRetired
	}
	if t.SourceKind != wantSource {
		return fmt.Errorf("%w: source kind %q does not match principal kind %q",
			ErrInvalidDirectoryEvidence, t.SourceKind, t.PrincipalKind)
	}
	if err := validateDirectoryID(t.SourceID, "directory source id"); err != nil {
		return err
	}
	if t.PrincipalKind == DirectoryPrincipalIdentity && t.SourceID != t.PrincipalRef {
		return fmt.Errorf("%w: Identity source id must equal principal ref",
			ErrInvalidDirectoryEvidence)
	}
	// Only the Go zero value is the public no-workspace sentinel. ID.IsZero also
	// recognizes the all-zero UUID for legacy callers, but admitting both here
	// would give one logical recipient two pre-codec representations.
	if t.WorkspaceRef != "" {
		if err := validateDirectoryID(t.WorkspaceRef, "workspace ref"); err != nil {
			return err
		}
	}
	if t.ResultingEpoch < 1 {
		return fmt.Errorf("%w: resulting epoch must be at least one",
			ErrInvalidDirectoryEvidence)
	}
	if t.Cause != wantCause {
		return fmt.Errorf("%w: retirement cause %q does not match principal kind %q",
			ErrInvalidDirectoryEvidence, t.Cause, t.PrincipalKind)
	}
	if err := validateRetirementCommon(t.Actor, t.RetiredAt); err != nil {
		return err
	}
	return t.AuditAnchor.Validate(
		AuditActionDirectoryPrincipalRetire, DirectoryTombstoneKind, t.ID,
	)
}

func validateRetirementCommon(actor string, retiredAt Timestamp) error {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(actor) != actor {
		return fmt.Errorf("%w: retirement actor must be non-empty and canonical",
			ErrInvalidDirectoryEvidence)
	}
	if retiredAt.IsZero() {
		return fmt.Errorf("%w: retirement DB time is missing", ErrInvalidDirectoryEvidence)
	}
	return nil
}

func validateDirectoryID(id ID, what string) error {
	raw := id.String()
	u, err := uuid.Parse(raw)
	if err != nil || u.String() != raw || u.Version() != uuid.Version(7) ||
		u.Variant() != uuid.RFC4122 {
		return fmt.Errorf("%w: %s is not a canonical UUIDv7",
			ErrInvalidDirectoryEvidence, what)
	}
	return nil
}

func validateDirectoryTenant(tenant TenantID) error {
	if tenant.IsZero() || tenant.IsSystem() {
		return fmt.Errorf("%w: directory tenant must be a real UUIDv7",
			ErrInvalidDirectoryEvidence)
	}
	return validateDirectoryID(ID(tenant), "directory tenant")
}
