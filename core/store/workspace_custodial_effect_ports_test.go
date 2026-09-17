// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// The confined port matrix (P2 / W1).
//
// What it proves is the OBSERVABLE method set of the value ConfineWorkspace's
// single final step returns, over the complete input space: 32 base-capability
// subsets crossed with 16 optional-port subsets, 512 combinations. It counts no
// source declarations and names no generated type: a missing switch case shows
// up as a refusal, and a case that attaches the wrong port set shows up as a
// method that is present when it must be absent, or absent when it must be
// present.
//
// It drives the two production functions directly (confineWorkspace and
// forwardWorkspaceAuthorityPorts) rather than 512 hand-built raw scopes,
// because those two are exactly the seam ConfineWorkspace composes: the first
// chooses the base decorator from the five base capabilities, the second
// chooses the port set from the four ports. Feeding them the 32 and the 16
// inputs separately covers the same 512 outcomes with 48 test types.

const (
	custodyMatrixWorkspaceSlug = "ws-custody-matrix"
	custodyMatrixTenant        = model.TenantID("0192f7a0-0000-7000-8000-00000000c0de")
)

// custodyMatrixPorts is every optional capability the matrix installs, in one
// value. Which of its methods are REACHABLE is decided by the anonymous struct
// the builders wrap it in, never by this type.
type custodyMatrixPorts struct {
	bindings []CustodialEffectBinding
	readers  []BoundedReadOptions
}

func (p *custodyMatrixPorts) TransactionNow(context.Context) (model.Timestamp, error) {
	return model.Timestamp{}, nil
}
func (p *custodyMatrixPorts) LockTransaction(context.Context, string) error { return nil }
func (p *custodyMatrixPorts) LockAuthoritySnapshot(context.Context, []AuthorizationFactRef) error {
	return nil
}
func (p *custodyMatrixPorts) ReadDirectoryEpoch(context.Context) (model.DirectoryEpoch, error) {
	return model.DirectoryEpoch{}, nil
}
func (p *custodyMatrixPorts) ReadDirectoryTombstone(
	context.Context, DirectoryPrincipalRef,
) (DirectoryTombstoneWitness, bool, error) {
	return DirectoryTombstoneWitness{}, false, nil
}
func (p *custodyMatrixPorts) ReadAuthorizationEpoch(context.Context) (AuthorizationFactRef, error) {
	return AuthorizationFactRef{}, nil
}
func (p *custodyMatrixPorts) BumpAuthorizationEpoch(
	context.Context, AuthorizationFactRef,
) (AuthorizationFactRef, error) {
	return AuthorizationFactRef{}, nil
}
func (p *custodyMatrixPorts) LockAuthoritySnapshotBundle(context.Context, AuthoritySnapshotBundle) error {
	return nil
}
func (p *custodyMatrixPorts) LockDirectoryAuthoritySnapshot(context.Context, AuthoritySnapshotBundle) error {
	return nil
}
func (p *custodyMatrixPorts) NewBoundedReader(opts BoundedReadOptions) (BoundedReader, error) {
	p.readers = append(p.readers, opts)
	return nil, nil
}
func (p *custodyMatrixPorts) BindCustodialEffect(
	_ context.Context, b CustodialEffectBinding,
) (CustodialEffectHandle, error) {
	p.bindings = append(p.bindings, b)
	return nil, nil
}

// custodyMatrixScope is a raw Scope with NO optional capability of its own. The
// builders below add exactly the ones a case needs, so a capability that
// appears on a confined result and was not installed is a promotion defect.
type custodyMatrixScope struct{ workspaces custodyMatrixWorkspaceRepo }

func (s custodyMatrixScope) Tenant() model.TenantID { return custodyMatrixTenant }
func (s custodyMatrixScope) Org(context.Context) (model.Org, error) {
	return model.Org{}, nil
}
func (s custodyMatrixScope) SetOrgSettings(context.Context, map[string]any) (model.Org, error) {
	return model.Org{}, nil
}
func (s custodyMatrixScope) Agents() Repository[model.Agent]       { return nil }
func (s custodyMatrixScope) Sessions() Repository[model.Session]   { return nil }
func (s custodyMatrixScope) Providers() Repository[model.Provider] { return nil }
func (s custodyMatrixScope) Models() Repository[model.Model]       { return nil }
func (s custodyMatrixScope) MCPServers() Repository[model.MCPServer] {
	return nil
}
func (s custodyMatrixScope) Skills() Repository[model.Skill] { return nil }
func (s custodyMatrixScope) Tools() Repository[model.Tool]   { return nil }
func (s custodyMatrixScope) Resources() ResourceRepo         { return nil }
func (s custodyMatrixScope) Identities() MutableRepository[model.Identity] {
	return nil
}
func (s custodyMatrixScope) Policies() Repository[model.Policy]     { return nil }
func (s custodyMatrixScope) Costs() Repository[model.CostRecord]    { return nil }
func (s custodyMatrixScope) Evals() Repository[model.EvalResult]    { return nil }
func (s custodyMatrixScope) Findings() Repository[model.Finding]    { return nil }
func (s custodyMatrixScope) Health() Repository[model.HealthStatus] { return nil }
func (s custodyMatrixScope) Deployments() Repository[model.Deployment] {
	return nil
}
func (s custodyMatrixScope) Workspaces() Repository[model.Workspace] { return s.workspaces }
func (s custodyMatrixScope) AgentGroups() Repository[model.AgentGroup] {
	return nil
}
func (s custodyMatrixScope) AgentGroupMembers() Repository[model.AgentGroupMember] {
	return nil
}
func (s custodyMatrixScope) DefaultWorkspace(ctx context.Context) (model.Workspace, error) {
	return s.workspaces.Get(ctx, s.workspaces.defaultID)
}
func (s custodyMatrixScope) AccessEdges() AccessEdgeRepo               { return nil }
func (s custodyMatrixScope) Audit() AuditLog                           { return nil }
func (s custodyMatrixScope) EvidenceOperations() EvidenceOperationRepo { return nil }
func (s custodyMatrixScope) AccessEvidence() AccessEvidenceRepo        { return nil }
func (s custodyMatrixScope) Ext(model.Kind) (GenericRepo, error) {
	return nil, ErrUnknownEntity
}

// custodyMatrixWorkspaceRepo answers the two reads confineWorkspace performs
// before it builds a boundary: the target workspace and the tenant's default.
type custodyMatrixWorkspaceRepo struct {
	confinedID model.ID
	defaultID  model.ID
}

func (r custodyMatrixWorkspaceRepo) Get(_ context.Context, id model.ID) (model.Workspace, error) {
	switch id {
	case r.confinedID:
		return model.Workspace{BaseFields: model.BaseFields{ID: id}, Slug: custodyMatrixWorkspaceSlug}, nil
	case r.defaultID:
		return model.Workspace{BaseFields: model.BaseFields{ID: id}, Slug: model.DefaultWorkspaceSlug}, nil
	}
	return model.Workspace{}, ErrNotFound
}

func (r custodyMatrixWorkspaceRepo) List(context.Context, model.Query) ([]model.Workspace, model.Page, error) {
	return nil, model.Page{}, nil
}
func (r custodyMatrixWorkspaceRepo) Create(_ context.Context, w model.Workspace) (model.Workspace, error) {
	return w, nil
}
func (r custodyMatrixWorkspaceRepo) Update(_ context.Context, w model.Workspace) (model.Workspace, error) {
	return w, nil
}
func (r custodyMatrixWorkspaceRepo) Delete(context.Context, model.ID) error { return nil }

// TestConfinedPortSelectionCoversEveryBaseAndPortCombination drives all 512
// (base, port-set) pairs through the two production selection steps and checks
// the observable method set of every result.
func TestConfinedPortSelectionCoversEveryBaseAndPortCombination(t *testing.T) {
	ctx := context.Background()
	confinedID, defaultID := model.NewID(), model.NewID()
	raw := custodyMatrixScope{workspaces: custodyMatrixWorkspaceRepo{
		confinedID: confinedID, defaultID: defaultID,
	}}

	// The combinations are COUNTED and asserted, not implied by the loop bounds.
	// A receipt that says 512 because someone read two literals is a different
	// claim from one that says 512 because 512 results were checked, and the
	// distinction is the whole reason this test exists.
	combinations := 0
	distinctBases := map[string]struct{}{}
	for baseMask := uint8(0); baseMask < 32; baseMask++ {
		for portMask := uint8(0); portMask < 16; portMask++ {
			combinations++
			ports := &custodyMatrixPorts{}
			base, err := confineWorkspace(ctx, custodyMatrixBaseRaw(raw, baseMask, ports), confinedID)
			if err != nil {
				t.Fatalf("base %d: confine: %v", baseMask, err)
			}
			distinctBases[fmt.Sprintf("%T", base)] = struct{}{}
			out, err := forwardWorkspaceAuthorityPorts(base, custodyMatrixPortRaw(raw, portMask, ports))
			if err != nil {
				t.Fatalf("base %d ports %d: forward: %v", baseMask, portMask, err)
			}
			assertCustodyMatrixMethodSet(t, out, baseMask, portMask)

			// Confinement is carried INTO the ports, not merely alongside them.
			if portMask&custodyMatrixCustodyBit != 0 {
				claimer, ok := out.(CustodialEffectClaimer)
				if !ok {
					t.Fatalf("base %d ports %d: custody selected but absent", baseMask, portMask)
				}
				if _, err := claimer.BindCustodialEffect(ctx, CustodialEffectBinding{}); err != nil {
					t.Fatalf("base %d ports %d: bind: %v", baseMask, portMask, err)
				}
				if n := len(ports.bindings); n != 1 {
					t.Fatalf("base %d ports %d: %d bindings reached the raw claimer, want 1",
						baseMask, portMask, n)
				}
				got := ports.bindings[0]
				if !got.Confined() || got.ConfinedWorkspaceID() != confinedID {
					t.Fatalf("base %d ports %d: forwarded binding confined=%t workspace=%s, want true/%s",
						baseMask, portMask, got.Confined(), got.ConfinedWorkspaceID(), confinedID)
				}
			}
			// The factory keeps installing ITS boundary beside custody: a scope
			// that carries both must not lose either, which is the exact defect a
			// chained second attachment step would cause.
			if portMask&custodyMatrixFactoryBit != 0 {
				factory, ok := out.(BoundedReaderFactory)
				if !ok {
					t.Fatalf("base %d ports %d: factory selected but absent", baseMask, portMask)
				}
				if _, err := factory.NewBoundedReader(BoundedReadOptions{}); err != nil {
					t.Fatalf("base %d ports %d: new bounded reader: %v", baseMask, portMask, err)
				}
				if n := len(ports.readers); n != 1 {
					t.Fatalf("base %d ports %d: %d reader options reached the raw factory, want 1",
						baseMask, portMask, n)
				}
				if ports.readers[0].PolicyReadAllowed() {
					t.Fatalf("base %d ports %d: forwarded reader options carry no boundary",
						baseMask, portMask)
				}
			}
			// Nothing promotes the raw Scope out of the confinement.
			if _, widened := out.(interface{ RawScope() Scope }); widened {
				t.Fatalf("base %d ports %d: the confined result exposed a raw scope",
					baseMask, portMask)
			}
		}
	}
	if combinations != 512 || len(distinctBases) != 32 {
		t.Fatalf("checked %d combinations over %d distinct confined bases, want 512 over 32",
			combinations, len(distinctBases))
	}
	t.Logf("checked %d (base, port-set) combinations over %d distinct confined base types",
		combinations, len(distinctBases))
}

const (
	custodyMatrixFactoryBit = 1 << 2
	custodyMatrixCustodyBit = 1 << 3
)

// custodyMatrixFullRaw is the complete nine-capability raw scope.
type custodyMatrixFullRaw struct {
	Scope
	TransactionClock
	TransactionLocker
	AuthoritySnapshotLocker
	DirectorySnapshotReader
	AuthorizationEpochStore
	AuthoritySnapshotBundleLocker
	DirectoryAuthoritySnapshotLocker
	BoundedReaderFactory
	CustodialEffectClaimer
}

// assertCustodyMatrixMethodSet checks presence AND absence: a port that was not
// selected must not be assertable, which is the half a "does it still work"
// test cannot see.
func assertCustodyMatrixMethodSet(t *testing.T, out Scope, baseMask, portMask uint8) {
	t.Helper()
	_, hasClock := out.(TransactionClock)
	_, hasLocker := out.(TransactionLocker)
	_, hasAuthority := out.(AuthoritySnapshotLocker)
	_, hasDirectory := out.(DirectorySnapshotReader)
	_, hasEpoch := out.(AuthorizationEpochStore)
	base := []struct {
		name string
		got  bool
		want bool
	}{
		{"clock", hasClock, baseMask&1 != 0},
		{"locker", hasLocker, baseMask&2 != 0},
		{"authority", hasAuthority, baseMask&4 != 0},
		{"directory", hasDirectory, baseMask&8 != 0},
		{"authorization epoch", hasEpoch, baseMask&16 != 0},
	}
	for _, c := range base {
		if c.got != c.want {
			t.Fatalf("base %d ports %d: %s capability = %t, want %t",
				baseMask, portMask, c.name, c.got, c.want)
		}
	}
	_, hasBundle := out.(AuthoritySnapshotBundleLocker)
	_, hasDirectoryAuthority := out.(DirectoryAuthoritySnapshotLocker)
	_, hasFactory := out.(BoundedReaderFactory)
	_, hasCustody := out.(CustodialEffectClaimer)
	ports := []struct {
		name string
		got  bool
		want bool
	}{
		{"bundle locker", hasBundle, portMask&1 != 0},
		{"directory authority", hasDirectoryAuthority, portMask&2 != 0},
		{"bounded reader factory", hasFactory, portMask&custodyMatrixFactoryBit != 0},
		{"custodial effect", hasCustody, portMask&custodyMatrixCustodyBit != 0},
	}
	for _, c := range ports {
		if c.got != c.want {
			t.Fatalf("base %d ports %d: %s port = %t, want %t",
				baseMask, portMask, c.name, c.got, c.want)
		}
	}
	if out.Tenant() != custodyMatrixTenant {
		t.Fatalf("base %d ports %d: Scope methods do not reach the confined base", baseMask, portMask)
	}
}

// TestConfinedCustodyReconfinementAndBoundaryRefusal proves the two properties
// the per-port guard exists for: a same-workspace re-confinement is idempotent
// and loses NO port, and a different workspace is refused rather than
// retargeted.
func TestConfinedCustodyReconfinementAndBoundaryRefusal(t *testing.T) {
	ctx := context.Background()
	confinedID, defaultID, otherID := model.NewID(), model.NewID(), model.NewID()
	raw := custodyMatrixScope{workspaces: custodyMatrixWorkspaceRepo{
		confinedID: confinedID, defaultID: defaultID,
	}}
	ports := &custodyMatrixPorts{}
	// Every base capability and every port in ONE type: the shape a real SQL
	// tenantScope presents once the custodial relation is valid. It is written
	// out rather than composed from the two builders on purpose — wrapping a
	// wrapper embeds the Scope INTERFACE and would silently drop the inner
	// capabilities, which is the very defect the single attachment step exists
	// to prevent.
	full := custodyMatrixFullRaw{
		Scope: raw, TransactionClock: ports, TransactionLocker: ports,
		AuthoritySnapshotLocker: ports, DirectorySnapshotReader: ports,
		AuthorizationEpochStore: ports, AuthoritySnapshotBundleLocker: ports,
		DirectoryAuthoritySnapshotLocker: ports, BoundedReaderFactory: ports,
		CustodialEffectClaimer: ports,
	}

	first, err := ConfineWorkspace(ctx, full, confinedID)
	if err != nil {
		t.Fatalf("confine: %v", err)
	}
	assertCustodyMatrixMethodSet(t, first, 31, 15)

	second, err := ConfineWorkspace(ctx, first, confinedID)
	if err != nil {
		t.Fatalf("same-workspace re-confinement: %v", err)
	}
	if second != first {
		t.Fatalf("same-workspace re-confinement returned %T, want the identical scope", second)
	}
	assertCustodyMatrixMethodSet(t, second, 31, 15)

	if got, err := ConfineWorkspace(ctx, first, otherID); got != nil ||
		!errors.Is(err, ErrWorkspaceConfinement) {
		t.Fatalf("retarget to another workspace = %T, %v; want ErrWorkspaceConfinement", got, err)
	}

	// A re-confinement must not double-install the boundary either: the port is
	// the same value, so one bind still reaches the raw claimer once.
	if _, err := second.(CustodialEffectClaimer).BindCustodialEffect(ctx, CustodialEffectBinding{}); err != nil {
		t.Fatalf("bind after re-confinement: %v", err)
	}
	if n := len(ports.bindings); n != 1 || ports.bindings[0].ConfinedWorkspaceID() != confinedID {
		t.Fatalf("bindings after re-confinement = %d (%v)", n, ports.bindings)
	}
}

// TestCustodyPortRefusesAnUnknownConfinedDecorator proves the deny-closed
// default of the custody half of the selection step. A confined value that is
// not one of the named decorators — which is what a CHAINED second attachment
// step would hand it — is refused, never silently returned without the port.
func TestCustodyPortRefusesAnUnknownConfinedDecorator(t *testing.T) {
	ctx := context.Background()
	confinedID, defaultID := model.NewID(), model.NewID()
	raw := custodyMatrixScope{workspaces: custodyMatrixWorkspaceRepo{
		confinedID: confinedID, defaultID: defaultID,
	}}
	ports := &custodyMatrixPorts{}
	base, err := confineWorkspace(ctx, custodyMatrixBaseRaw(raw, 31, ports), confinedID)
	if err != nil {
		t.Fatalf("confine: %v", err)
	}
	// One attachment, then a SECOND on its anonymous result.
	once, err := forwardWorkspaceAuthorityPorts(base, custodyMatrixPortRaw(raw, 1, ports))
	if err != nil {
		t.Fatalf("first attachment: %v", err)
	}
	again, err := forwardWorkspaceAuthorityPorts(once, custodyMatrixPortRaw(raw, 15, ports))
	if again != nil || !errors.Is(err, ErrWorkspaceConfinement) {
		t.Fatalf("chained attachment = %T, %v; want ErrWorkspaceConfinement", again, err)
	}
}

// TestCustodialBindingBoundaryIsNotCallerSuppliable proves that a binding value
// a module constructs carries no confinement, and that only the forwarding
// wrapper can install one.
func TestCustodialBindingBoundaryIsNotCallerSuppliable(t *testing.T) {
	var caller CustodialEffectBinding
	if caller.Confined() || !caller.ConfinedWorkspaceID().IsZero() {
		t.Fatal("a caller-constructed binding carries a confinement")
	}
	desc := model.EntityDescriptor{
		Kind: "sessions.run", Table: "sessions_run",
		Fields:           []model.FieldSpec{{Name: "authz_workspace_id", Kind: model.KindUUID, Nullable: true}},
		WorkspaceLineage: model.WorkspaceLineageSpec{Column: "authz_workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden},
	}
	if _, confined, err := caller.WorkspaceConstraint(desc); confined || err != nil {
		t.Fatalf("unconfined constraint = confined %t, %v; want false/nil", confined, err)
	}
	ws, def := model.NewID(), model.NewID()
	bound := caller.confine(workspaceBoundary{id: ws, slug: custodyMatrixWorkspaceSlug, defaultID: def})
	filter, confined, err := bound.WorkspaceConstraint(desc)
	if err != nil || !confined {
		t.Fatalf("confined constraint = %t, %v", confined, err)
	}
	if filter.Column != "authz_workspace_id" || filter.Op != model.OpEq || filter.Value != ws.String() {
		t.Fatalf("forced predicate = %+v, want an equality on the lineage column", filter)
	}
	// The run relation hides an unset lineage; it must never resolve to the
	// tenant default, which is the opposite spelling sessions.identity uses.
	if inside, ok := bound.WorkspaceOwns(desc, ""); inside || !ok {
		t.Fatalf("unset lineage inside=%t readable=%t, want hidden and readable", inside, ok)
	}
	if inside, ok := bound.WorkspaceOwns(desc, ws.String()); !inside || !ok {
		t.Fatalf("own lineage inside=%t readable=%t", inside, ok)
	}
	if inside, ok := bound.WorkspaceOwns(desc, def.String()); inside || !ok {
		t.Fatalf("other-workspace lineage inside=%t readable=%t", inside, ok)
	}
	if inside, ok := bound.WorkspaceOwns(desc, "not-a-uuid"); inside || ok {
		t.Fatalf("unreadable lineage inside=%t readable=%t, want denied and unreadable", inside, ok)
	}
}

// custodyMatrixBaseRaw returns a raw Scope exposing exactly the subset of the
// FIVE base capabilities named by mask. confineWorkspace selects one of its 32
// named decorators from that subset, so driving mask 0..31 reaches every one of
// them by construction rather than by naming them.
func custodyMatrixBaseRaw(base Scope, mask uint8, p *custodyMatrixPorts) Scope {
	switch mask {
	case 0:
		return struct{ Scope }{base}
	case 1:
		return struct {
			Scope
			TransactionClock
		}{base, p}
	case 2:
		return struct {
			Scope
			TransactionLocker
		}{base, p}
	case 3:
		return struct {
			Scope
			TransactionClock
			TransactionLocker
		}{base, p, p}
	case 4:
		return struct {
			Scope
			AuthoritySnapshotLocker
		}{base, p}
	case 5:
		return struct {
			Scope
			TransactionClock
			AuthoritySnapshotLocker
		}{base, p, p}
	case 6:
		return struct {
			Scope
			TransactionLocker
			AuthoritySnapshotLocker
		}{base, p, p}
	case 7:
		return struct {
			Scope
			TransactionClock
			TransactionLocker
			AuthoritySnapshotLocker
		}{base, p, p, p}
	case 8:
		return struct {
			Scope
			DirectorySnapshotReader
		}{base, p}
	case 9:
		return struct {
			Scope
			TransactionClock
			DirectorySnapshotReader
		}{base, p, p}
	case 10:
		return struct {
			Scope
			TransactionLocker
			DirectorySnapshotReader
		}{base, p, p}
	case 11:
		return struct {
			Scope
			TransactionClock
			TransactionLocker
			DirectorySnapshotReader
		}{base, p, p, p}
	case 12:
		return struct {
			Scope
			AuthoritySnapshotLocker
			DirectorySnapshotReader
		}{base, p, p}
	case 13:
		return struct {
			Scope
			TransactionClock
			AuthoritySnapshotLocker
			DirectorySnapshotReader
		}{base, p, p, p}
	case 14:
		return struct {
			Scope
			TransactionLocker
			AuthoritySnapshotLocker
			DirectorySnapshotReader
		}{base, p, p, p}
	case 15:
		return struct {
			Scope
			TransactionClock
			TransactionLocker
			AuthoritySnapshotLocker
			DirectorySnapshotReader
		}{base, p, p, p, p}
	case 16:
		return struct {
			Scope
			AuthorizationEpochStore
		}{base, p}
	case 17:
		return struct {
			Scope
			TransactionClock
			AuthorizationEpochStore
		}{base, p, p}
	case 18:
		return struct {
			Scope
			TransactionLocker
			AuthorizationEpochStore
		}{base, p, p}
	case 19:
		return struct {
			Scope
			TransactionClock
			TransactionLocker
			AuthorizationEpochStore
		}{base, p, p, p}
	case 20:
		return struct {
			Scope
			AuthoritySnapshotLocker
			AuthorizationEpochStore
		}{base, p, p}
	case 21:
		return struct {
			Scope
			TransactionClock
			AuthoritySnapshotLocker
			AuthorizationEpochStore
		}{base, p, p, p}
	case 22:
		return struct {
			Scope
			TransactionLocker
			AuthoritySnapshotLocker
			AuthorizationEpochStore
		}{base, p, p, p}
	case 23:
		return struct {
			Scope
			TransactionClock
			TransactionLocker
			AuthoritySnapshotLocker
			AuthorizationEpochStore
		}{base, p, p, p, p}
	case 24:
		return struct {
			Scope
			DirectorySnapshotReader
			AuthorizationEpochStore
		}{base, p, p}
	case 25:
		return struct {
			Scope
			TransactionClock
			DirectorySnapshotReader
			AuthorizationEpochStore
		}{base, p, p, p}
	case 26:
		return struct {
			Scope
			TransactionLocker
			DirectorySnapshotReader
			AuthorizationEpochStore
		}{base, p, p, p}
	case 27:
		return struct {
			Scope
			TransactionClock
			TransactionLocker
			DirectorySnapshotReader
			AuthorizationEpochStore
		}{base, p, p, p, p}
	case 28:
		return struct {
			Scope
			AuthoritySnapshotLocker
			DirectorySnapshotReader
			AuthorizationEpochStore
		}{base, p, p, p}
	case 29:
		return struct {
			Scope
			TransactionClock
			AuthoritySnapshotLocker
			DirectorySnapshotReader
			AuthorizationEpochStore
		}{base, p, p, p, p}
	case 30:
		return struct {
			Scope
			TransactionLocker
			AuthoritySnapshotLocker
			DirectorySnapshotReader
			AuthorizationEpochStore
		}{base, p, p, p, p}
	case 31:
		return struct {
			Scope
			TransactionClock
			TransactionLocker
			AuthoritySnapshotLocker
			DirectorySnapshotReader
			AuthorizationEpochStore
		}{base, p, p, p, p, p}
	}
	panic("unreachable mask")
}

// custodyMatrixPortRaw returns a raw Scope exposing exactly the subset of the
// FOUR optional ports named by mask. forwardWorkspaceAuthorityPorts asserts
// these four interfaces on the raw scope and nothing else, so mask 0..15 is the
// complete port input space.
func custodyMatrixPortRaw(base Scope, mask uint8, p *custodyMatrixPorts) Scope {
	switch mask {
	case 0:
		return struct{ Scope }{base}
	case 1:
		return struct {
			Scope
			AuthoritySnapshotBundleLocker
		}{base, p}
	case 2:
		return struct {
			Scope
			DirectoryAuthoritySnapshotLocker
		}{base, p}
	case 3:
		return struct {
			Scope
			AuthoritySnapshotBundleLocker
			DirectoryAuthoritySnapshotLocker
		}{base, p, p}
	case 4:
		return struct {
			Scope
			BoundedReaderFactory
		}{base, p}
	case 5:
		return struct {
			Scope
			AuthoritySnapshotBundleLocker
			BoundedReaderFactory
		}{base, p, p}
	case 6:
		return struct {
			Scope
			DirectoryAuthoritySnapshotLocker
			BoundedReaderFactory
		}{base, p, p}
	case 7:
		return struct {
			Scope
			AuthoritySnapshotBundleLocker
			DirectoryAuthoritySnapshotLocker
			BoundedReaderFactory
		}{base, p, p, p}
	case 8:
		return struct {
			Scope
			CustodialEffectClaimer
		}{base, p}
	case 9:
		return struct {
			Scope
			AuthoritySnapshotBundleLocker
			CustodialEffectClaimer
		}{base, p, p}
	case 10:
		return struct {
			Scope
			DirectoryAuthoritySnapshotLocker
			CustodialEffectClaimer
		}{base, p, p}
	case 11:
		return struct {
			Scope
			AuthoritySnapshotBundleLocker
			DirectoryAuthoritySnapshotLocker
			CustodialEffectClaimer
		}{base, p, p, p}
	case 12:
		return struct {
			Scope
			BoundedReaderFactory
			CustodialEffectClaimer
		}{base, p, p}
	case 13:
		return struct {
			Scope
			AuthoritySnapshotBundleLocker
			BoundedReaderFactory
			CustodialEffectClaimer
		}{base, p, p, p}
	case 14:
		return struct {
			Scope
			DirectoryAuthoritySnapshotLocker
			BoundedReaderFactory
			CustodialEffectClaimer
		}{base, p, p, p}
	case 15:
		return struct {
			Scope
			AuthoritySnapshotBundleLocker
			DirectoryAuthoritySnapshotLocker
			BoundedReaderFactory
			CustodialEffectClaimer
		}{base, p, p, p, p}
	}
	panic("unreachable mask")
}
