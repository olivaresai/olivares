// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// registry holds every registered entity descriptor (core and module) keyed by
// kind and by table, plus the module migration filesystems. It validates module
// descriptors against the naming, reserved-column, type and isolation rules. It
// is closed (no further registration) once the store finishes building schema.
type registry struct {
	byKind  map[model.Kind]model.EntityDescriptor
	byTable map[string]model.Kind
	order   []model.Kind // registration order (core first, then modules)
	modMig  []moduleMig  // module migration filesystems
	// invariants holds one module's required schema objects by namespace, declared
	// through SchemaInvariants (the S5-A surface this branch adds).
	invariants map[string]moduleSchemaInvariant
	// rollout holds the staged controls a module declared (store.RolloutControl).
	// The engine classifies them once, before it creates any module table.
	rollout               []store.RolloutControl
	workspaceInitializers []store.WorkspaceInitializer
	closed                bool
}

// moduleMig is a module's migration filesystem mounted under its namespace.
type moduleMig struct {
	namespace string
	fsys      fs.FS
}

func newRegistry() *registry {
	return &registry{
		byKind:     make(map[model.Kind]model.EntityDescriptor),
		invariants: make(map[string]moduleSchemaInvariant),
		byTable:    make(map[string]model.Kind),
	}
}

// registerCore records an engine-owned descriptor. It trusts the catalog (the
// "core" namespace is reserved for exactly these) but still guards against an
// internal duplicate.
func (r *registry) registerCore(d model.EntityDescriptor) error {
	if _, dup := r.byKind[d.Kind]; dup {
		return fmt.Errorf("duplicate core kind %q", d.Kind)
	}
	if k, dup := r.byTable[d.Table]; dup {
		return fmt.Errorf("core table %q already used by %q", d.Table, k)
	}
	// Core descriptors must obey the same reserved-column and type rules as
	// modules; a collision here would otherwise surface only as a migration
	// failure (e.g. a duplicate "version" column).
	for _, f := range d.Fields {
		if model.IsReservedColumn(f.Name) {
			return fmt.Errorf("core kind %q field %q is a reserved base column", d.Kind, f.Name)
		}
		if !f.Kind.Valid() {
			return fmt.Errorf("core kind %q field %q has an invalid kind", d.Kind, f.Name)
		}
	}
	// The lineage rules apply to core exactly as to modules: confinement reads
	// the same spec on both sides, so a core catalog typo must fail at boot and
	// not become a silently unconfinable entity.
	if err := validateWorkspaceLineage(d); err != nil {
		return fmt.Errorf("core kind %q: %v", d.Kind, err)
	}
	if err := validateAuthorizationFact(d); err != nil {
		return fmt.Errorf("core kind %q: %v", d.Kind, err)
	}
	r.byKind[d.Kind] = d
	r.byTable[d.Table] = d.Kind
	r.order = append(r.order, d.Kind)
	return nil
}

// Register validates and records a module descriptor. It implements
// store.ExtensionRegistry.
func (r *registry) Register(d model.EntityDescriptor) error {
	if r.closed {
		return fmt.Errorf("%w: registration is closed", store.ErrInvalidDescriptor)
	}
	if err := r.validateModule(d); err != nil {
		return fmt.Errorf("%w: %v", store.ErrInvalidDescriptor, err)
	}
	r.byKind[d.Kind] = d
	r.byTable[d.Table] = d.Kind
	r.order = append(r.order, d.Kind)
	return nil
}

// Migrations records a module migration filesystem under its namespace. It
// implements store.ExtensionRegistry.
func (r *registry) Migrations(namespace string, fsys fs.FS) error {
	if r.closed {
		return fmt.Errorf("%w: registration is closed", store.ErrInvalidDescriptor)
	}
	if !isNamespace(namespace) {
		return fmt.Errorf("%w: invalid namespace %q", store.ErrInvalidDescriptor, namespace)
	}
	for _, mm := range r.modMig {
		if mm.namespace == namespace {
			return fmt.Errorf("%w: duplicate migration namespace %q", store.ErrInvalidDescriptor, namespace)
		}
	}
	r.modMig = append(r.modMig, moduleMig{namespace: namespace, fsys: fsys})
	return nil
}

// WorkspaceInitializer records a module's atomic workspace bootstrap. It
// implements store.ExtensionRegistry.
func (r *registry) WorkspaceInitializer(initializer store.WorkspaceInitializer) error {
	if r.closed {
		return fmt.Errorf("%w: registration is closed", store.ErrInvalidDescriptor)
	}
	if err := initializer.Validate(); err != nil {
		return err
	}
	for _, existing := range r.workspaceInitializers {
		if existing.Key == initializer.Key {
			return fmt.Errorf(
				"%w: duplicate workspace initializer key %q",
				store.ErrInvalidDescriptor, initializer.Key,
			)
		}
	}
	r.workspaceInitializers = append(r.workspaceInitializers, initializer)
	return nil
}

// RolloutControl records a staged control's declaration. It implements
// store.ExtensionRegistry.
//
// The witness table is NOT checked here, because a module may legitimately declare
// the control before it registers the table it names. It is checked once at close
// (see validateRolloutControls), and it is checked rather than trusted because a
// typo would classify every upgrade as a fresh install — the unsafe direction,
// since a fresh classification grandfathers nothing and an upgrade misread as fresh
// is an estate whose existing entitlements stop working without warning.
func (r *registry) RolloutControl(c store.RolloutControl) error {
	if r.closed {
		return fmt.Errorf("%w: registration is closed", store.ErrInvalidDescriptor)
	}
	if err := c.Validate(); err != nil {
		return err
	}
	for _, existing := range r.rollout {
		if existing.Key == c.Key {
			return fmt.Errorf("%w: duplicate rollout control key %q", store.ErrInvalidDescriptor, c.Key)
		}
	}
	r.rollout = append(r.rollout, c)
	return nil
}

// validateRolloutControls checks every declared control against the tables that
// were actually registered. It runs when registration closes, so ordering within a
// module's hook does not matter.
func (r *registry) validateRolloutControls() error {
	for _, c := range r.rollout {
		kind, ok := r.byTable[c.WitnessTable]
		if !ok {
			return fmt.Errorf("%w: rollout control %q names witness table %q, which no module registered", store.ErrInvalidDescriptor, c.Key, c.WitnessTable)
		}
		// The witness must belong to the module that owns the control: a control keyed
		// "eventing.egress.destination.v1" witnessed by another module's table would
		// classify this deployment on somebody else's history, and the two modules can
		// be enabled independently.
		if ns := kind.Namespace(); ns != controlNamespace(c.Key) {
			return fmt.Errorf("%w: rollout control %q is witnessed by table %q, which belongs to namespace %q", store.ErrInvalidDescriptor, c.Key, c.WitnessTable, ns)
		}
	}
	return nil
}

// validateWorkspaceInitializers binds every initializer to a namespace that
// actually registered an entity. It runs at registry close so declaration order
// within a module hook is irrelevant.
func (r *registry) validateWorkspaceInitializers() error {
	for _, initializer := range r.workspaceInitializers {
		namespace := controlNamespace(initializer.Key)
		owned := false
		for _, kind := range r.order {
			if kind.Namespace() == namespace && namespace != model.CoreNamespace {
				owned = true
				break
			}
		}
		if !owned {
			return fmt.Errorf(
				"%w: workspace initializer %q has no registered entity in namespace %q",
				store.ErrInvalidDescriptor, initializer.Key, namespace,
			)
		}
	}
	return nil
}

// validateRetainedDescriptors binds a mutable descriptor's tenant-retention
// lifecycle to the database object that makes hard delete impossible. It runs
// only when registration closes: modules may register their descriptors before
// SchemaInvariants, and rejecting that valid order in Register would make the
// declaration order part of the security contract.
//
// Append-only descriptors are already protected by engine-owned guards on both
// dialects, so they need no module invariant. A mutable retained descriptor does:
// the exact <table>_no_delete trigger, declared by the descriptor's own namespace
// on every supported engine, with a full canonical SHA-256 of its live definition.
func (r *registry) validateRetainedDescriptors() error {
	for _, kind := range r.order {
		d := r.byKind[kind]
		if !d.RetainOnTenantDrop || d.AppendOnly {
			continue
		}

		namespace := kind.Namespace()
		declared, ok := r.invariants[namespace]
		if !ok {
			return fmt.Errorf(
				"%w: retained mutable kind %q has no schema invariants in its namespace %q",
				store.ErrInvalidDescriptor, kind, namespace,
			)
		}
		wantName := d.Table + "_no_delete"
		for _, engine := range store.SupportedEngines() {
			var match *store.SchemaTrigger
			for i := range declared.byEngine[engine] {
				candidate := &declared.byEngine[engine][i]
				if candidate.Name == wantName && candidate.Table == d.Table {
					match = candidate
					break
				}
			}
			if match == nil {
				return fmt.Errorf(
					"%w: retained mutable kind %q requires trigger %q on table %q for engine %q in namespace %q",
					store.ErrInvalidDescriptor, kind, wantName, d.Table, engine, namespace,
				)
			}
			if !canonicalSHA256Hex(match.DefinitionSHA256) {
				return fmt.Errorf(
					"%w: retained mutable kind %q trigger %q on engine %q requires a full lowercase SHA-256 definition digest",
					store.ErrInvalidDescriptor, kind, wantName, engine,
				)
			}
		}
	}
	return nil
}

func canonicalSHA256Hex(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

// controlNamespace is the leading dotted segment of a control key, which by
// convention is the owning module's namespace.
func controlNamespace(key string) string {
	if i := strings.IndexByte(key, '.'); i > 0 {
		return key[:i]
	}
	return key
}

// rolloutControls returns the declared controls in registration order.
func (r *registry) rolloutControls() []store.RolloutControl {
	return r.rollout
}

// registeredWorkspaceInitializers returns a stable key-ordered copy. Module
// registration order must not become an implicit cross-module lock order.
func (r *registry) registeredWorkspaceInitializers() []store.WorkspaceInitializer {
	result := append([]store.WorkspaceInitializer(nil), r.workspaceInitializers...)
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

// validateModule enforces the module descriptor rules (ARCHITECTURE.md).
func (r *registry) validateModule(d model.EntityDescriptor) error {
	if !d.Kind.Valid() {
		return fmt.Errorf("kind %q must be <namespace>.<entity>", d.Kind)
	}
	ns := d.Kind.Namespace()
	if ns == model.CoreNamespace {
		return fmt.Errorf("namespace %q is reserved for the engine", ns)
	}
	if d.AppendOnly && d.SoftDelete {
		return fmt.Errorf("append-only and soft-delete are mutually exclusive")
	}
	// CHECK expressions are interpolated verbatim into DDL, so they are a
	// core-catalog-only feature: a module-supplied expression would be a
	// DDL-injection surface the registry cannot validate structurally.
	if len(d.Checks) > 0 {
		return fmt.Errorf("check constraints are reserved for core descriptors")
	}
	// Table must be "<namespace>_<snake>" so module tables never collide with
	// core's bare names and are attributable to their module.
	if !strings.HasPrefix(d.Table, ns+"_") || len(d.Table) <= len(ns)+1 {
		return fmt.Errorf("table %q must be %q-prefixed (%s_…)", d.Table, ns, ns)
	}
	if !isTableName(d.Table) {
		return fmt.Errorf("table %q is not a valid identifier", d.Table)
	}
	if _, dup := r.byKind[d.Kind]; dup {
		return fmt.Errorf("kind %q already registered", d.Kind)
	}
	if k, dup := r.byTable[d.Table]; dup {
		return fmt.Errorf("table %q already used by %q", d.Table, k)
	}
	// Bound the table name so engine-derived identifiers (indexes, triggers, the
	// per-module tracking table) stay within Postgres's 63-byte limit.
	if len(d.Table) > maxModuleTableLen {
		return fmt.Errorf("table %q exceeds %d characters", d.Table, maxModuleTableLen)
	}
	for _, f := range d.Fields {
		if model.IsReservedColumn(f.Name) {
			return fmt.Errorf("field %q is a reserved base column", f.Name)
		}
		if !isTableName(f.Name) {
			return fmt.Errorf("field %q is not a valid identifier", f.Name)
		}
		if !f.Kind.Valid() {
			return fmt.Errorf("field %q has an invalid kind", f.Name)
		}
		if f.Redact && f.Kind != model.KindText && f.Kind != model.KindBytes {
			return fmt.Errorf("field %q: redact requires a text or bytes kind", f.Name)
		}
	}
	if err := validateWorkspaceLineage(d); err != nil {
		return err
	}
	if err := validateAuthorizationFact(d); err != nil {
		return err
	}
	return r.validateIndexes(d)
}

func validateAuthorizationFact(d model.EntityDescriptor) error {
	touch := d.AuthorizationLeaseFence
	if d.AuthorizationFact && d.AuthorizationLockOrder == 0 {
		return fmt.Errorf("authorization fact requires a non-zero lock order")
	}
	if !d.AuthorizationFact && d.AuthorizationLockOrder != 0 {
		return fmt.Errorf("authorization lock order requires authorization fact opt-in")
	}
	if d.AuthorizationFact && !allowedAuthorizationFactKind(d.Kind) {
		return fmt.Errorf("entity %q is not in the authorization fact allowlist", d.Kind)
	}
	if !touch.Declared() {
		return nil
	}
	if !d.AuthorizationFact {
		return fmt.Errorf("authorization lease/fence touch requires authorization fact opt-in")
	}
	if touch.SubjectColumn == "" || touch.FenceColumn == "" || touch.StateColumn == "" ||
		touch.ActiveValue == "" || touch.DeadlineColumn == "" {
		return fmt.Errorf("authorization lease/fence touch declaration is incomplete")
	}
	if len(touch.ActiveValue) > 128 {
		return fmt.Errorf("authorization lease/fence active value is too long")
	}
	fields := map[string]model.FieldSpec{}
	for _, field := range d.Fields {
		fields[field.Name] = field
	}
	columns := []string{
		touch.SubjectColumn, touch.FenceColumn, touch.StateColumn, touch.DeadlineColumn,
	}
	seenColumns := make(map[string]struct{}, len(columns))
	for _, name := range columns {
		if _, duplicate := seenColumns[name]; duplicate {
			return fmt.Errorf("authorization lease/fence columns must be distinct")
		}
		seenColumns[name] = struct{}{}
	}
	for name, want := range map[string]model.SQLKind{
		touch.SubjectColumn:  model.KindText,
		touch.FenceColumn:    model.KindInt,
		touch.StateColumn:    model.KindText,
		touch.DeadlineColumn: model.KindTimestamp,
	} {
		field, ok := fields[name]
		if !ok {
			return fmt.Errorf("authorization lease/fence column %q is not declared", name)
		}
		if field.Kind != want || field.Nullable || field.Redact {
			return fmt.Errorf(
				"authorization lease/fence column %q must be non-null, unredacted %s",
				name, want,
			)
		}
	}
	return nil
}

func allowedAuthorizationFactKind(kind model.Kind) bool {
	switch kind {
	case "core.identity", "core.agent", model.DirectoryEpochKind,
		model.AuthorizationEpochKind,
		"governance.nhi_lifecycle", "sessions.claim":
		return true
	default:
		return false
	}
}

// validateWorkspaceLineage checks an entity's workspace-lineage declaration
// (B-03). A partial or unreadable declaration is rejected rather than
// ignored: row-level confinement filters through this spec alone, so a spec the
// engine cannot act on would silently degrade to "no lineage" — which reads as
// deny-closed for a confined caller, but only after the entity has already been
// registered as if it were confinable. An undeclared spec (the zero value) is
// valid: it means the entity is engine/tenant-wide and is refused to a confined
// principal.
func validateWorkspaceLineage(d model.EntityDescriptor) error {
	s := d.WorkspaceLineage
	if !s.Declared() {
		// A spec is either fully absent or fully specified; half of one is a
		// declaration whose meaning nobody can state.
		if s.Encoding != "" || s.Unset != "" {
			return fmt.Errorf("workspace lineage: encoding/unset declared without a column")
		}
		return nil
	}
	if !s.Encoding.ValidEncoding() {
		return fmt.Errorf("workspace lineage: unknown encoding %q", s.Encoding)
	}
	if !s.Unset.ValidUnset() {
		return fmt.Errorf("workspace lineage: unknown unset semantics %q", s.Unset)
	}
	if model.IsReservedColumn(s.Column) {
		return fmt.Errorf("workspace lineage: column %q is a reserved base column", s.Column)
	}
	var f model.FieldSpec
	found := false
	for _, c := range d.Fields {
		if c.Name == s.Column {
			f, found = c, true
			break
		}
	}
	if !found {
		return fmt.Errorf("workspace lineage: column %q is not a declared field", s.Column)
	}
	if f.Redact {
		return fmt.Errorf("workspace lineage: column %q is redacted, so its value cannot be compared", s.Column)
	}
	switch s.Encoding {
	case model.WorkspaceLineageID:
		if f.Kind != model.KindUUID && f.Kind != model.KindText {
			return fmt.Errorf("workspace lineage: id encoding needs a uuid or text column, %q is not", s.Column)
		}
	case model.WorkspaceLineageSlug:
		if f.Kind != model.KindText {
			return fmt.Errorf("workspace lineage: slug encoding needs a text column, %q is not", s.Column)
		}
	}
	// "Unset means the default workspace" is only expressible if the column can
	// actually BE unset; on a NOT NULL column the rule would never fire and the
	// declaration would quietly mean something else than it says.
	if s.Unset == model.WorkspaceUnsetMeansDefault && !f.Nullable {
		return fmt.Errorf("workspace lineage: column %q must be nullable to declare unset=default", s.Column)
	}
	return nil
}

// maxModuleTableLen bounds a module table name so derived identifiers stay under
// the Postgres 63-byte cap.
const maxModuleTableLen = 40

// validateIndexes checks a module's index specs. Index names and columns are
// otherwise interpolated verbatim into DDL, so they must be validated like every
// other identifier; and a UNIQUE index that does not lead with tenant_id would
// span all tenants — coupling tenants and leaking existence — so it is rejected.
func (r *registry) validateIndexes(d model.EntityDescriptor) error {
	for _, ix := range d.Indexes {
		if !isTableName(ix.Name) {
			return fmt.Errorf("index name %q is not a valid identifier", ix.Name)
		}
		if len(ix.Columns) == 0 {
			return fmt.Errorf("index %q declares no columns", ix.Name)
		}
		for _, c := range ix.Columns {
			if !descriptorHasColumn(d, c) {
				return fmt.Errorf("index %q references unknown column %q", ix.Name, c)
			}
		}
		if ix.Unique && ix.Columns[0] != model.ColTenantID {
			return fmt.Errorf("unique index %q must lead with %q for tenant isolation", ix.Name, model.ColTenantID)
		}
	}
	return nil
}

// descriptorHasColumn reports whether name is a real column of the descriptor
// (a base column actually present, or a declared entity column).
func descriptorHasColumn(d model.EntityDescriptor, name string) bool {
	for _, c := range d.AllColumns() {
		if c == name {
			return true
		}
	}
	return false
}

// descriptors returns all registered descriptors in registration order.
func (r *registry) descriptors() []model.EntityDescriptor {
	out := make([]model.EntityDescriptor, 0, len(r.order))
	for _, k := range r.order {
		out = append(out, r.byKind[k])
	}
	return out
}

// moduleDescriptors returns the descriptors that are NOT core, in registration
// order — the tables the engine generates from a descriptor.
func (r *registry) moduleDescriptors() []model.EntityDescriptor {
	var out []model.EntityDescriptor
	for _, k := range r.order {
		if k.Namespace() != model.CoreNamespace {
			out = append(out, r.byKind[k])
		}
	}
	return out
}

// lookup returns a descriptor by kind.
func (r *registry) lookup(kind model.Kind) (model.EntityDescriptor, bool) {
	d, ok := r.byKind[kind]
	return d, ok
}

// tenantTables returns every table that carries a tenant_id (i.e. every entity
// table plus audit_events), used by the boot self-test.
func (r *registry) tenantTables() []string {
	out := make([]string, 0, len(r.order)+2)
	for _, k := range r.order {
		out = append(out, r.byKind[k].Table)
	}
	out = append(out, auditTable, auditHeadsTable)
	sort.Strings(out)
	return out
}

// appendOnlyTables returns every table the engine guards as append-only: each
// descriptor that declares AppendOnly plus audit_events, which is append-only by
// construction rather than by descriptor (the dialect emits its immutability
// trigger and revoke directly).
//
// It is the set whose ACL must not include UPDATE/DELETE/TRUNCATE for the
// application role. audit_heads and the audit-spool bookkeeping tables are
// deliberately absent: they are mutable state (a chain head advances, a counter is
// updated), tenant-guarded but not evidence.
func (r *registry) appendOnlyTables() []string {
	out := make([]string, 0, len(r.order)+1)
	for _, k := range r.order {
		if r.byKind[k].AppendOnly {
			out = append(out, r.byKind[k].Table)
		}
	}
	out = append(out, auditTable)
	sort.Strings(out)
	return out
}

// mutableTenantTables returns the tenant tables that are NOT append-only — the ones
// the application role legitimately needs full DML on. Splitting the two sets is
// what lets the boot privilege check demand opposite things of them without
// contradicting itself: DML present here, DML absent on the append-only side.
func (r *registry) mutableTenantTables() []string {
	appendOnly := make(map[string]bool, len(r.order))
	for _, t := range r.appendOnlyTables() {
		appendOnly[t] = true
	}
	out := make([]string, 0, len(r.order)+1)
	for _, t := range r.tenantTables() {
		if !appendOnly[t] {
			out = append(out, t)
		}
	}
	return out
}

// isNamespace reports whether s is a valid module namespace identifier, short
// enough that the derived per-module tracking table stays under the Postgres
// 63-byte identifier limit.
func isNamespace(s string) bool {
	return isTableName(s) && s != model.CoreNamespace && len(s) <= 32
}

// isTableName reports whether s is a lowercase snake identifier [a-z][a-z0-9_]*.
func isTableName(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// moduleSchemaInvariant is one module's engine-specific required-trigger set.
type moduleSchemaInvariant struct {
	namespace string
	byEngine  map[store.Engine][]store.SchemaTrigger
}

// SchemaInvariants records engine-specific trigger requirements for one module.
// It is part of store.ExtensionRegistry — deliberately, because as a separate
// optional interface a registry that did not implement it would have discarded a
// module's security invariants silently — and follows the registry's same
// closed-after-build discipline.
//
// The previous comment claimed it implements store.SchemaInvariantRegistry. There is
// no such type: it was the shape this surface had before it moved onto the main
// interface, and the name survived the move.
func (r *registry) SchemaInvariants(
	namespace string,
	byEngine map[store.Engine][]store.SchemaTrigger,
) error {
	if r.closed {
		return fmt.Errorf("%w: registration is closed", store.ErrInvalidDescriptor)
	}
	if !isNamespace(namespace) {
		return fmt.Errorf("%w: invalid namespace %q", store.ErrInvalidDescriptor, namespace)
	}
	if _, exists := r.invariants[namespace]; exists {
		return fmt.Errorf("%w: duplicate schema-invariant namespace %q", store.ErrInvalidDescriptor, namespace)
	}
	if len(byEngine) == 0 {
		return fmt.Errorf("%w: namespace %q declares no schema invariants", store.ErrInvalidDescriptor, namespace)
	}
	// Every supported engine must be covered. An under-declared map is the same
	// silent hole as no declaration at all: the self-test looks up the ACTIVE
	// engine, finds nothing, and returns success — so a store running on the
	// omitted engine verifies no trigger while the module still reports healthy.
	for _, engine := range store.SupportedEngines() {
		if len(byEngine[engine]) == 0 {
			return fmt.Errorf(
				"%w: namespace %q declares schema invariants but none for engine %q; a module that needs an invariant needs it on every supported engine",
				store.ErrInvalidDescriptor, namespace, engine)
		}
	}

	copied := make(map[store.Engine][]store.SchemaTrigger, len(byEngine))
	for engine, triggers := range byEngine {
		if engine != store.EngineSQLite && engine != store.EnginePostgres {
			return fmt.Errorf("%w: namespace %q declares invariants for unsupported engine %q",
				store.ErrInvalidDescriptor, namespace, engine)
		}
		if len(triggers) == 0 {
			return fmt.Errorf("%w: namespace %q declares an empty trigger set for engine %q",
				store.ErrInvalidDescriptor, namespace, engine)
		}
		// Identity is (table, name), not name alone. PostgreSQL only requires a
		// trigger name to be unique PER TABLE, so rejecting a name globally is an
		// invented restriction: it would stop a module from installing, say, a
		// `..._no_truncate` guard on two of its own tables, and it contradicts the
		// structured key the self-test now looks triggers up by.
		type triggerIdentity struct{ table, name string }
		seen := make(map[triggerIdentity]bool, len(triggers))
		copiedTriggers := make([]store.SchemaTrigger, 0, len(triggers))
		for _, trigger := range triggers {
			if !isTableName(trigger.Name) {
				return fmt.Errorf("%w: invalid trigger name %q", store.ErrInvalidDescriptor, trigger.Name)
			}
			if !isTableName(trigger.Table) {
				return fmt.Errorf("%w: invalid trigger table %q", store.ErrInvalidDescriptor, trigger.Table)
			}
			if err := validateSchemaTriggerTransitions(engine, trigger); err != nil {
				return fmt.Errorf(
					"%w: trigger %q on table %q for engine %q: %v",
					store.ErrInvalidDescriptor, trigger.Name, trigger.Table, engine, err,
				)
			}
			id := triggerIdentity{table: trigger.Table, name: trigger.Name}
			if seen[id] {
				return fmt.Errorf("%w: duplicate trigger %q on table %q for engine %q",
					store.ErrInvalidDescriptor, trigger.Name, trigger.Table, engine)
			}
			seen[id] = true
			for otherNamespace, other := range r.invariants {
				for _, existing := range other.byEngine[engine] {
					if existing.Name == trigger.Name && existing.Table == trigger.Table {
						return fmt.Errorf("%w: trigger %q on table %q for engine %q already declared by namespace %q",
							store.ErrInvalidDescriptor, trigger.Name, trigger.Table, engine, otherNamespace)
					}
				}
			}
			copiedTriggers = append(copiedTriggers, cloneSchemaTrigger(trigger))
		}
		copied[engine] = copiedTriggers
	}
	r.invariants[namespace] = moduleSchemaInvariant{namespace: namespace, byEngine: copied}
	return nil
}

// validateSchemaTriggerTransitions validates the digest chain without consulting
// migration files. Binding each version to an actual file happens when the active
// engine's migration plan is assembled, before any module migration is applied.
func validateSchemaTriggerTransitions(engine store.Engine, trigger store.SchemaTrigger) error {
	if len(trigger.Transitions) == 0 {
		return nil
	}
	if !canonicalSHA256Hex(trigger.DefinitionSHA256) {
		return fmt.Errorf("transitions require a full lowercase SHA-256 current definition digest")
	}
	for i, transition := range trigger.Transitions {
		if transition.MigrationVersion <= 0 {
			return fmt.Errorf(
				"transition %d has non-positive migration version %d",
				i+1, transition.MigrationVersion,
			)
		}
		if i > 0 && transition.MigrationVersion <= trigger.Transitions[i-1].MigrationVersion {
			return fmt.Errorf(
				"transition versions must be strictly increasing and unique: %d follows %d",
				transition.MigrationVersion, trigger.Transitions[i-1].MigrationVersion,
			)
		}
		if !canonicalSHA256Hex(transition.PreviousDefinitionSHA256) {
			return fmt.Errorf(
				"transition at migration v%d requires a full lowercase SHA-256 previous definition digest",
				transition.MigrationVersion,
			)
		}
		next := trigger.DefinitionSHA256
		if i+1 < len(trigger.Transitions) {
			next = trigger.Transitions[i+1].PreviousDefinitionSHA256
		}
		if transition.PreviousDefinitionSHA256 == next {
			return fmt.Errorf(
				"transition at migration v%d has identical previous and next definition digests",
				transition.MigrationVersion,
			)
		}
		identity := transition.PostgresFunctionIdentity
		if engine == store.EngineSQLite {
			if identity != nil {
				return fmt.Errorf(
					"transition at migration v%d declares a PostgreSQL function identity for SQLite",
					transition.MigrationVersion,
				)
			}
			continue
		}
		if identity == nil {
			return fmt.Errorf(
				"transition at migration v%d requires a PostgreSQL function identity change",
				transition.MigrationVersion,
			)
		}
		if !validPostgresTransitionIdentifier(identity.PreviousName) {
			return fmt.Errorf(
				"transition at migration v%d has an invalid previous PostgreSQL function name",
				transition.MigrationVersion,
			)
		}
		if !validPostgresTransitionIdentifier(identity.NextName) {
			return fmt.Errorf(
				"transition at migration v%d has an invalid next PostgreSQL function name",
				transition.MigrationVersion,
			)
		}
		if identity.PreviousName == identity.NextName {
			return fmt.Errorf(
				"transition at migration v%d must change PostgreSQL function identity",
				transition.MigrationVersion,
			)
		}
		if i > 0 {
			previousIdentity := trigger.Transitions[i-1].PostgresFunctionIdentity
			if previousIdentity != nil && previousIdentity.NextName != identity.PreviousName {
				return fmt.Errorf(
					"transition at migration v%d starts from PostgreSQL function %q, but the preceding transition ends at %q",
					transition.MigrationVersion, identity.PreviousName, previousIdentity.NextName,
				)
			}
		}
	}
	return nil
}

// PostgreSQL stores identifiers in name, whose default NAMEDATALEN permits at
// most 63 UTF-8 bytes. Empty, invalid or NUL-bearing values cannot be represented
// injectively in the catalog. Otherwise quoted PostgreSQL identifiers are allowed:
// transition functions may legitimately contain punctuation that is unsafe to
// parse from regprocedure text.
func validPostgresTransitionIdentifier(value string) bool {
	return value != "" && len(value) <= 63 && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00')
}

func cloneSchemaTrigger(trigger store.SchemaTrigger) store.SchemaTrigger {
	cloned := trigger
	cloned.Transitions = make([]store.SchemaTriggerTransition, len(trigger.Transitions))
	for i, transition := range trigger.Transitions {
		cloned.Transitions[i] = transition
		if transition.PostgresFunctionIdentity != nil {
			identity := *transition.PostgresFunctionIdentity
			cloned.Transitions[i].PostgresFunctionIdentity = &identity
		}
	}
	return cloned
}

// schemaInvariants returns the active engine's required triggers in stable
// namespace/registration order for boot verification.
func (r *registry) schemaInvariants(engine store.Engine) []registeredSchemaTrigger {
	namespaces := make([]string, 0, len(r.invariants))
	for namespace := range r.invariants {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	var out []registeredSchemaTrigger
	for _, namespace := range namespaces {
		for _, trigger := range r.invariants[namespace].byEngine[engine] {
			out = append(out, registeredSchemaTrigger{
				namespace: namespace, SchemaTrigger: cloneSchemaTrigger(trigger),
			})
		}
	}
	return out
}

// registeredSchemaTrigger retains the declaring namespace for diagnostics and
// for identifying the module tables whose app-role privileges must be probed.
type registeredSchemaTrigger struct {
	namespace string
	store.SchemaTrigger
}

// invariantBoundaryTables returns append-only tables owned by a module that
// declared security triggers. These are the module fact tables whose explicit
// SELECT/INSERT grants are part of the trigger boundary in an owner/app split.
func (r *registry) invariantBoundaryTables(engine store.Engine) []string {
	namespaces := make(map[string]bool)
	for _, invariant := range r.schemaInvariants(engine) {
		namespaces[invariant.namespace] = true
	}
	var out []string
	for _, kind := range r.order {
		desc := r.byKind[kind]
		if desc.AppendOnly && namespaces[kind.Namespace()] {
			out = append(out, desc.Table)
		}
	}
	sort.Strings(out)
	return out
}
