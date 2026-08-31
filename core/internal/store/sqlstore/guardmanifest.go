// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// guardmanifest.go is the BINARY'S half of the guard manifest: what this edition of the
// code declares its append-only guards to be.
//
// The split is deliberate and both halves are needed (step-2 design §1). The binary is
// the authority of INTENTION — which objects this edition manages and which edges it
// knows how to execute. The database is the authority of HISTORY — which edition was
// accepted, which objects were ever managed, their canonical bytes, their receipts.
//
// A binary-only manifest would lose the tables of every module dropped from a build,
// which is precisely the set the ACL scope already preserves by unioning the registry
// with the live catalog (appendonly_acl.go). A database-only manifest would let anyone
// who edits an object AND its golden together promote drift to "canonical". Neither half
// alone is safe, so neither half alone is authoritative.
//
// Nothing here talks to a database. That is what makes it testable without one, and it
// is why the census is a MEASUREMENT of the closed registry rather than a constant: see
// buildGuardManifest.

// guardManifestFormat is how to interpret the canonical bytes, and it changes ONLY when
// the encoding or the catalog projection changes — never when an entry is added.
//
// Separating it from the code epoch is what makes an old binary refuse a newer database
// safely: a format it does not understand is refused before any DDL (ErrGuardFormatAhead),
// while a format it does understand with a different epoch is a manifest question.
// Folding the two into one number would make every added table look like an encoding
// change and every encoding change look like an added table.
const guardManifestFormat int64 = 1

// guardCodeEpoch is the newest edition this binary knows how to declare.
//
// It is POSITIVE and MONOTONIC, and it is a compiled constant on purpose: the epoch is a
// statement about the code, so a value read from the database or from configuration would
// let a deployment claim an edition its binary cannot execute. Changing any active
// canonical byte WITHOUT changing this is ErrGuardManifestDrift — which is the whole
// point of having it.
//
// Epoch 2 adds the directory-retirement evidence relations used by K3. Epoch 3 adds the
// five append-only communication evidence relations introduced by Slice F. Epoch 4 adds
// the two append-only protocol replay/subscription relations introduced by K5. A modular
// binary advances only when it registers the complete delta and every predecessor delta;
// a partial census is refused. Thus the constant is a maximum, not permission for a
// core-only binary to claim an edition whose module relations it does not carry.
//
// An increment is not, by itself, authority to move a database:
// guardManifestEditionEdge derives the one exact predecessor for each known edge. Every
// other older digest is refused.
const guardCodeEpoch int64 = 4

// The following constants are the complete relation delta authorized by the 1 -> 2 edge.
//
// The registry speaks in SQL table names while the public descriptors speak in kinds:
// these are core.user_tombstone and core.directory_tombstone respectively. Keeping this
// list beside the edition constant makes an addition to the registry inert with respect
// to the edge unless it is named here deliberately. Both entries are mandatory: an
// incomplete closed registry is refused before migration rather than permitted to write
// an epoch-2 digest that the complete binary would later call same-epoch drift.
const (
	guardEpoch2UserTombstoneTable      = "core_user_tombstone"
	guardEpoch2DirectoryTombstoneTable = "core_directory_tombstone"
)

// These five relations are the complete delta authorized by the 2 -> 3 edge. They are
// append-only outcome/provenance records, not the mutable communication carriers. The
// closed-census selector below deliberately admits zero or all five and nothing between.
var guardEpoch3CommunicationTables = [...]string{
	"sessions_message_audience",
	"sessions_message_audience_recipient",
	"sessions_message_ack",
	"sessions_decision_response",
	"sessions_communication_command",
}

// These two relations are the complete delta authorized by the 3 -> 4 edge. They are
// immutable protocol evidence introduced together by K5: the replay authority and the
// durable subscription event stream. Their mutable carrier/head tables are deliberately
// absent because they do not receive the engine's append-only guard.
var guardEpoch4ProtocolTables = [...]string{
	"sessions_communication_replay_guard",
	"sessions_protocol_subscription_event",
}

// guardEpochMax is the largest epoch the durable representation can hold.
//
// BIGINT, not a conceptual uint64: the ledger column is BIGINT and prestate.Epoch is
// int64, so a uint64 epoch would have no faithful home at either end. The range is
// declared rather than assumed so a manifest that overflows it is refused here instead of
// being silently truncated by the driver.
const guardEpochMax int64 = math.MaxInt64

// The PostgreSQL majors this PRODUCT supports, which is not the same claim as the majors this
// projection has been MEASURED on, and conflating the two was a real overclaim.
//
// 15..18 is the product's ratified support range (decision D1), and the projection has now been
// RUN against all four — see certifiedPostgresMajors for the dated measurement. This paragraph
// used to say the repository had executed it against 15 alone; that was true when it was
// written and stopped being true in the same file, which is the way a comment usually starts
// lying. The honest statement of the remaining gap is narrower and lives at
// certifiedPostgresMajors: the list records a run, and nothing in the local gate re-runs it
// unless the matrix DSNs are set.
//
// The refusal that remains is still worth having: outside the supported range a major may add a
// structural field the comparator does not read, and a field nobody reads is a field in which
// two genuinely different functions compare equal. Refusing there is honest; claiming 16-18 were
// verified would not be.
const (
	supportedPostgresMajorMin = 15
	supportedPostgresMajorMax = 18
	// verifiedPostgresMajor is the major the DECLARED shape was generated from.
	//
	// It is the provenance of dialect.GuardControlPlaneShapePostgres — which server's catalog
	// those literal strings were read out of — and it is no longer the same question as "on which
	// majors may the strongest comparison run". See certifiedPostgresMajors.
	verifiedPostgresMajor = 15
)

// guardProducer says who declared an entry. The engine derives one per append-only table
// it registers; a module may declare its own explicitly, which is why the type exists
// even though the first edition has only engine entries.
type guardProducer string

const guardProducerEngine guardProducer = "engine"

// guardKey is an entry's stable identity: the three raw catalog components.
//
// NEVER flattened with dots and never rendered as quoted SQL. PostgreSQL identifiers may
// contain any byte but NUL, so `"a.b"."c"` and `"a"."b.c"` are two different relations
// that a dotted key would give one identity — the same defect the lock plan's NUL
// separator exists to prevent (see plannedLock.relation). Here the separation is
// structural: three fields, length-prefixed independently by the canonical encoder.
type guardKey struct {
	Schema   string
	Relation string
	Trigger  string
}

func (k guardKey) String() string {
	return quoteIdent(k.Schema) + "." + quoteIdent(k.Relation) + " trigger " + quoteIdent(k.Trigger)
}

// less orders keys by their three RAW components, which is the manifest's declared entry
// order. Raw, not quoted: quoting is a rendering concern and would make the order depend
// on which identifiers happen to need escaping.
func (k guardKey) less(o guardKey) bool {
	if k.Schema != o.Schema {
		return k.Schema < o.Schema
	}
	if k.Relation != o.Relation {
		return k.Relation < o.Relation
	}
	return k.Trigger < o.Trigger
}

func (k guardKey) canon(w *canonWriter) {
	w.str(k.Schema)
	w.str(k.Relation)
	w.str(k.Trigger)
}

// guardRelationForm is the relation's eligibility projection: what kind of relation may
// carry a managed guard at all.
//
// The fields are the ones that DECIDE, and two of them are here because a plausible
// shorter predicate is wrong. relkind='r' does NOT exclude partitions — a partitioned
// parent is 'p' but every LEAF partition is 'r' and is otherwise indistinguishable from
// an ordinary table, so relispartition is load-bearing. And inheritance in either
// direction is refused, because a lock on a parent is not a lock on a child: attributing
// a leaf's lock to its parent is a measured way to lose a declared lock entirely
// (migrationfootprint.go).
type guardRelationForm struct {
	Kind        string // pg_class.relkind
	Persistence string // pg_class.relpersistence
	IsPartition bool   // pg_class.relispartition
	HasParent   bool   // EXISTS in pg_inherits as a child
	HasChild    bool   // EXISTS in pg_inherits as a parent
}

func (r guardRelationForm) canon(w *canonWriter) {
	w.str(r.Kind)
	w.str(r.Persistence)
	w.boolean(r.IsPartition)
	w.boolean(r.HasParent)
	w.boolean(r.HasChild)
}

// guardTriggerForm is every structural field of pg_trigger EXCEPT tgenabled.
//
// The exclusion is what makes the O -> A transition representable at all. 'O' and 'A' can
// be byte-identical in every other respect while the expected state differs, so a single
// footprint covering both would make a legitimate transition look like a mutation. The
// code already keeps GuardMatchesCanonical and GuardEnableState apart for the same reason
// (migrationunit.go); this is that separation carried into the hash.
//
// AttrCount and ArgsBytes are counts rather than the raw arrays, and that is a measured
// necessity rather than a shortcut: `BEFORE UPDATE OF a OR DELETE` and
// `BEFORE UPDATE OR DELETE` have the SAME tgtype (27 on 15.18, measured) and are told
// apart ONLY by tgattr — `{2}` against empty. A predicate reading tgtype alone adopts a
// lookalike that watches one column and lets every other column be updated.
type guardTriggerForm struct {
	ParentID     string // tgparentid, as text
	Type         int64  // tgtype
	IsInternal   bool   // tgisinternal
	ConstrRelID  string // tgconstrrelid
	ConstrIndID  string // tgconstrindid
	Constraint   string // tgconstraint
	Deferrable   bool   // tgdeferrable
	InitDeferred bool   // tginitdeferred
	NArgs        int64  // tgnargs
	AttrCount    int64  // array_length(tgattr, 1), 0 when empty
	ArgsBytes    int64  // octet_length(tgargs), 0 when empty
	Qual         optText
	OldTable     optText
	NewTable     optText
}

func (t guardTriggerForm) canon(w *canonWriter) {
	w.str(t.ParentID)
	w.i64(t.Type)
	w.boolean(t.IsInternal)
	w.str(t.ConstrRelID)
	w.str(t.ConstrIndID)
	w.str(t.Constraint)
	w.boolean(t.Deferrable)
	w.boolean(t.InitDeferred)
	w.i64(t.NArgs)
	w.i64(t.AttrCount)
	w.i64(t.ArgsBytes)
	w.opt(t.Qual)
	w.opt(t.OldTable)
	w.opt(t.NewTable)
}

// guardFunctionForm is the structural projection of the trigger function.
//
// It is the whole of pg_proc that decides behavior, including the fields that look
// cosmetic. prosecdef changes whose privileges the body runs with; provolatile and
// proparallel change when the planner may skip a call; proconfig can set a GUC for the
// call. A comparator that read only the name and the body would accept a function with
// the right source and the wrong execution semantics.
//
// Owner and ACL are deliberately ABSENT. They are diagnostic: a legitimate deployment may
// own its schema with any role, so folding ownership into the canonical bytes would make
// every such deployment report drift. What the app role may DO is the ACL verifier's
// question, answered from the application pool where the answer is meaningful.
type guardFunctionForm struct {
	Schema           string
	Name             string
	Kind             string // prokind
	ReturnTypeSchema string
	ReturnTypeName   string
	ReturnsSet       bool
	Language         string
	NArgs            int64
	NArgDefaults     int64
	Variadic         string // provariadic, as text
	ArgTypesCount    int64
	AllArgTypesNull  bool
	ArgModesNull     bool
	ArgNamesNull     bool
	ArgDefaultsNull  bool
	Src              string
	Bin              optText
	SQLBody          optText
	SecurityDefiner  bool
	LeakProof        bool
	Strict           bool
	Volatile         string
	Parallel         string
	Cost             float64
	Rows             float64
	Support          string // prosupport, as text
	TransformsNull   bool
	ConfigNull       bool
}

func (f guardFunctionForm) canon(w *canonWriter) {
	w.str(f.Schema)
	w.str(f.Name)
	w.str(f.Kind)
	w.str(f.ReturnTypeSchema)
	w.str(f.ReturnTypeName)
	w.boolean(f.ReturnsSet)
	w.str(f.Language)
	w.i64(f.NArgs)
	w.i64(f.NArgDefaults)
	w.str(f.Variadic)
	w.i64(f.ArgTypesCount)
	w.boolean(f.AllArgTypesNull)
	w.boolean(f.ArgModesNull)
	w.boolean(f.ArgNamesNull)
	w.boolean(f.ArgDefaultsNull)
	w.str(f.Src)
	w.opt(f.Bin)
	w.opt(f.SQLBody)
	w.boolean(f.SecurityDefiner)
	w.boolean(f.LeakProof)
	w.boolean(f.Strict)
	w.str(f.Volatile)
	w.str(f.Parallel)
	w.float(f.Cost)
	w.float(f.Rows)
	w.str(f.Support)
	w.boolean(f.TransformsNull)
	w.boolean(f.ConfigNull)
}

// guardDefinition is the whole structural projection of one guard, tgenabled excluded.
type guardDefinition struct {
	Relation guardRelationForm
	Trigger  guardTriggerForm
	Function guardFunctionForm
}

// definitionDigest is the definition_sha256 of the step-2 contract: relation + trigger +
// function, WITHOUT the enable state.
func (d guardDefinition) definitionDigest(key guardKey) ([32]byte, error) {
	w := newCanonWriter(canonDomainDefinition, guardManifestFormat)
	key.canon(w)
	d.Relation.canon(w)
	d.Trigger.canon(w)
	d.Function.canon(w)
	return w.sum()
}

// guardSpec is one manifest entry: the identity, who declared it, its structural
// definition, the state it must end in, and which legacy states may be adopted.
type guardSpec struct {
	Key                 guardKey
	Producer            guardProducer
	Definition          guardDefinition
	DesiredEnableState  string
	LegacyAllowedStates []string
	// DefinitionSHA256 and SpecSHA256 are computed by the constructor, never supplied.
	DefinitionSHA256 [32]byte
	SpecSHA256       [32]byte
}

// specDigest is spec_sha256: the definition digest plus the desired state, the legacy
// policy and the producer.
//
// It is a SECOND layer rather than a longer first one because the two answer different
// questions. "Is this object the one the manifest describes?" must not change when the
// expected state does, or an O -> A transition would look like a redefinition. "Is this
// the entry the rollout authorized?" must change, or one edition's approval would ratify
// another's policy.
func (s guardSpec) specDigest() ([32]byte, error) {
	w := newCanonWriter(canonDomainEntry, guardManifestFormat)
	s.Key.canon(w)
	w.bytes32(s.DefinitionSHA256)
	w.str(s.DesiredEnableState)
	w.list(len(s.LegacyAllowedStates))
	for _, st := range s.LegacyAllowedStates {
		w.str(st)
	}
	w.str(string(s.Producer))
	return w.sum()
}

// guardManifest is this binary's declared edition.
type guardManifest struct {
	Format    int64
	CodeEpoch int64
	// Specs are ordered by Key. The order is part of the canonical bytes, so it must not
	// depend on registration order — see buildGuardManifest.
	Specs      []guardSpec
	CodeSHA256 [32]byte
}

// manifestDigest is code_sha256: the fingerprint of every active entry of this edition.
func (m guardManifest) manifestDigest() ([32]byte, error) {
	w := newCanonWriter(canonDomainManifest, m.Format)
	w.i64(m.CodeEpoch)
	w.list(len(m.Specs))
	for _, s := range m.Specs {
		s.Key.canon(w)
		w.bytes32(s.SpecSHA256)
	}
	return w.sum()
}

// lookup returns the spec for a key.
func (m guardManifest) lookup(k guardKey) (guardSpec, bool) {
	// Linear over ~46 sorted entries, called a handful of times per boot. A map would
	// duplicate the identity rule that guardKey already owns, and a second place holding
	// that rule is a second place for it to drift.
	for _, s := range m.Specs {
		if s.Key == k {
			return s, true
		}
	}
	return guardSpec{}, false
}

// guardTriggerSuffix is the name the engine gives every append-only guard.
//
// It matches what the dialect emits today (`<table>_immutable`, postgres.go), and the
// match is CHECKED rather than assumed: buildGuardManifest derives the name with this
// constant and a regression compares the manifest's expected trigger names against the
// DDL the dialect actually renders. Two constants that must agree, with nothing checking
// they do, is how a manifest ends up describing objects nothing creates.
const guardTriggerSuffix = "_immutable"

// canonicalGuardDefinition is the definition the engine's own DDL produces, for a
// relation and its guard.
//
// Every constant here is a consequence of the statements in
// core/internal/store/dialect/postgres.go, and each is stated with its source:
//
//   - tgtype 27 = ROW(1) | BEFORE(2) | DELETE(8) | UPDATE(16), which is exactly
//     `BEFORE UPDATE OR DELETE ... FOR EACH ROW`.
//   - prosrc is the body between the dollar quotes of TenancyStmts, byte for byte,
//     leading and trailing newline included.
//   - procost 100 and prorows 0 are the CREATE FUNCTION defaults for a non-set-returning
//     function.
//   - provolatile 'v' and proparallel 'u' are likewise the defaults, and they are
//     recorded because the DDL does not state them — a future edit that adds STABLE or
//     PARALLEL SAFE must be a manifest change, not a silent one.
func canonicalGuardDefinition() guardDefinition {
	return guardDefinition{
		Relation: guardRelationForm{
			Kind:        "r",
			Persistence: "p",
		},
		Trigger: guardTriggerForm{
			ParentID:    "0",
			Type:        27,
			ConstrRelID: "0",
			ConstrIndID: "0",
			Constraint:  "0",
			// Qual, OldTable and NewTable are NULL: no WHEN clause and no transition
			// tables. optText's zero value is exactly that, and it is distinct from an
			// empty string by construction.
		},
		Function: guardFunctionForm{
			Schema:           guardSchema,
			Name:             guardBlockMutationFn,
			Kind:             "f",
			ReturnTypeSchema: "pg_catalog",
			ReturnTypeName:   "trigger",
			Language:         "plpgsql",
			Variadic:         "0",
			AllArgTypesNull:  true,
			ArgModesNull:     true,
			ArgNamesNull:     true,
			ArgDefaultsNull:  true,
			Src:              guardBlockMutationBody,
			Volatile:         "v",
			Parallel:         "u",
			Cost:             100,
			Rows:             0,
			Support:          "0",
			TransformsNull:   true,
			ConfigNull:       true,
		},
	}
}

// guardBlockMutationBody is the exact prosrc the engine's trigger function carries.
//
// Spelled as an explicit escape sequence rather than a raw literal so the leading and
// trailing newlines are visible: they are part of the bytes PostgreSQL stores, and a
// comparison that trimmed them would accept a function whose body had been reflowed —
// which is to say, edited.
const guardBlockMutationBody = "\nBEGIN\n  RAISE EXCEPTION 'table is append-only';\nEND;\n"

// buildGuardManifest derives this edition's manifest from the tables the registry
// declares append-only.
//
// tables is registry.appendOnlyTables() — the descriptors that declare AppendOnly plus
// audit_events, which the registry adds expressly. The count is therefore a MEASUREMENT
// of a closed registry and never a constant: at the time of writing it is 46 (45
// descriptors plus audit_events), and a regression reports N rather than freezing it,
// because freezing it would mean a table added by a module silently stops being managed
// while the test still passes.
//
// The refusals are the constructor's whole value:
//
//   - a duplicate key, which would put two entries in charge of one object;
//   - an empty or NUL-bearing identifier, which cannot be a PostgreSQL relation and whose
//     acceptance would poison the canonical identity;
//   - two different footprints for the SAME shared function, which is how a second
//     producer could quietly redefine the function every other entry depends on.
//
// The output is sorted by key, so it cannot depend on registration order. That is not
// tidiness: the order is inside the canonical bytes, so an order-dependent manifest would
// change code_sha256 when a module was enabled in a different sequence — reporting drift
// for a binary whose declarations are identical.
func buildGuardManifest(tables []string) (guardManifest, error) {
	epoch, err := guardManifestEpochForCensus(tables)
	if err != nil {
		return guardManifest{}, err
	}
	return buildGuardManifestAtEpoch(tables, epoch)
}

// guardManifestEpochForCensus keeps the core/module boundary explicit. Core-only and
// pre-Slice-F registries remain epoch 2, the complete Slice-F census advances to epoch 3,
// and the complete K5 protocol evidence census over that predecessor advances to epoch 4.
// A partial delta, or a later delta without its predecessor, is refused before any
// migration can record it.
func guardManifestEpochForCensus(tables []string) (int64, error) {
	epoch3Present := make(map[string]bool, len(guardEpoch3CommunicationTables))
	epoch4Present := make(map[string]bool, len(guardEpoch4ProtocolTables))
	for _, table := range tables {
		if guardEpoch3Adds(table) {
			epoch3Present[table] = true
		}
		if guardEpoch4Adds(table) {
			epoch4Present[table] = true
		}
	}
	if len(epoch4Present) > 0 && len(epoch4Present) != len(guardEpoch4ProtocolTables) {
		return 0, partialGuardEpochCensusError(4, epoch4Present, guardEpoch4ProtocolTables[:])
	}
	if len(epoch3Present) > 0 && len(epoch3Present) != len(guardEpoch3CommunicationTables) {
		return 0, partialGuardEpochCensusError(3, epoch3Present, guardEpoch3CommunicationTables[:])
	}
	if len(epoch4Present) == len(guardEpoch4ProtocolTables) {
		if len(epoch3Present) != len(guardEpoch3CommunicationTables) {
			return 0, fmt.Errorf("sqlstore: guard edition 4 census is complete but its epoch-3 predecessor is absent; refusing to authorize a 2 -> 4 edge")
		}
		return 4, nil
	}
	if len(epoch3Present) == len(guardEpoch3CommunicationTables) {
		return 3, nil
	}
	return 2, nil
}

func partialGuardEpochCensusError(epoch int64, present map[string]bool, required []string) error {
	missing := make([]string, 0, len(required)-len(present))
	for _, table := range required {
		if !present[table] {
			missing = append(missing, table)
		}
	}
	return fmt.Errorf("sqlstore: guard edition %d census is partial: found %d of %d append-only relations and is missing %v; refusing to record a same-epoch manifest that a complete module build would call drift",
		epoch, len(present), len(required), missing)
}

// buildGuardManifestAtEpoch is the single constructor for current and historical
// editions. Historical bytes are never literals copied into a second implementation:
// the exact same canonicaliser receives the exact predecessor census.
func buildGuardManifestAtEpoch(tables []string, epoch int64) (guardManifest, error) {
	m := guardManifest{Format: guardManifestFormat, CodeEpoch: epoch}
	if m.CodeEpoch <= 0 || m.CodeEpoch > guardEpochMax {
		return guardManifest{}, fmt.Errorf("sqlstore: guard manifest declares code epoch %d, which is outside 1..%d",
			m.CodeEpoch, guardEpochMax)
	}
	def := canonicalGuardDefinition()

	seen := make(map[guardKey]bool, len(tables))
	// One shared function, one footprint. The map is keyed by the function's IDENTITY and
	// holds its DEFINITION digest, so two entries naming the same function with different
	// bytes are refused rather than the second silently winning.
	fnDigests := make(map[string][32]byte, 1)

	for _, table := range tables {
		if strings.TrimSpace(table) == "" {
			return guardManifest{}, fmt.Errorf("sqlstore: guard manifest was handed an empty table name")
		}
		if strings.ContainsRune(table, 0) {
			return guardManifest{}, fmt.Errorf("sqlstore: guard manifest was handed the table name %q, which contains a NUL byte and cannot be a PostgreSQL identifier", table)
		}
		key := guardKey{Schema: guardSchema, Relation: table, Trigger: table + guardTriggerSuffix}
		if seen[key] {
			return guardManifest{}, fmt.Errorf("sqlstore: guard manifest names %s twice; two entries for one object means neither is in charge of it", key)
		}
		seen[key] = true

		spec := guardSpec{
			Key:      key,
			Producer: guardProducerEngine,
			// The relation and trigger halves are per-entry only in their names, which
			// live in Key; the structural fields are identical across the edition because
			// one dialect emits them all.
			Definition: def,
			// EVERY evidence guard of this edition wants ALWAYS. Certified on 15.18: a
			// guard left at 'O' let a publisher UPDATE apply on a logical-replication
			// subscriber with ZERO errors — the evidence mutated in silence — while 'A'
			// preserved the row and raised three apply errors. 'O' is therefore an
			// adoptable legacy PRESTATE and never a target.
			DesiredEnableState: guardStateAlways,
			// Adoption may take an existing guard under management from either state the
			// engine can have produced, and from no other. 'D' and 'R' are named to be
			// refused elsewhere; listing them here would make a disabled guard adoptable
			// as managed evidence.
			LegacyAllowedStates: []string{guardStateOrigin, guardStateAlways},
		}

		fnIdentity := spec.Definition.Function.Schema + "\x00" + spec.Definition.Function.Name
		fnDigest, err := functionDigest(spec.Definition.Function)
		if err != nil {
			return guardManifest{}, err
		}
		if prev, ok := fnDigests[fnIdentity]; ok && prev != fnDigest {
			return guardManifest{}, fmt.Errorf("sqlstore: guard manifest declares two different definitions for the shared function %s.%s; one of them would silently redefine the function every other entry depends on",
				spec.Definition.Function.Schema, spec.Definition.Function.Name)
		}
		fnDigests[fnIdentity] = fnDigest

		spec.DefinitionSHA256, err = spec.Definition.definitionDigest(spec.Key)
		if err != nil {
			return guardManifest{}, err
		}
		spec.SpecSHA256, err = spec.specDigest()
		if err != nil {
			return guardManifest{}, err
		}
		m.Specs = append(m.Specs, spec)
	}

	sort.Slice(m.Specs, func(i, j int) bool { return m.Specs[i].Key.less(m.Specs[j].Key) })

	var err error
	if m.CodeSHA256, err = m.manifestDigest(); err != nil {
		return guardManifest{}, err
	}
	return m, nil
}

// guardManifestEdge is the only forward edition transition this binary can authorize.
// Additions are sorted in target-manifest order.
type guardManifestEdge struct {
	From      guardManifest
	To        guardManifest
	Additions []guardSpec
}

func (e guardManifestEdge) authorizes(format, epoch int64, digest [32]byte) bool {
	return format == e.From.Format && epoch == e.From.CodeEpoch && digest == e.From.CodeSHA256
}

// guardManifestEditionEdge derives the immediate predecessor from the CLOSED current
// census by removing exactly the relations that current.CodeEpoch adds. This is
// intentionally stricter than accepting "any smaller epoch": if any other registry entry
// or canonical byte differs from the database that ran the predecessor, its digest does
// not match From and there is no edge.
func guardManifestEditionEdge(current guardManifest) (guardManifestEdge, bool, error) {
	if current.Format != guardManifestFormat || current.CodeEpoch < 2 || current.CodeEpoch > guardCodeEpoch {
		return guardManifestEdge{}, false, nil
	}
	if missing := missingGuardTablesForEpoch(current, current.CodeEpoch); len(missing) != 0 {
		// An incomplete build has no edge at all. In particular, it must not be able
		// to persist an epoch with an empty delta and later call the real additions
		// same-epoch drift.
		return guardManifestEdge{}, false, nil
	}

	predecessorTables := make([]string, 0, len(current.Specs))
	additions := make([]guardSpec, 0, len(current.Specs))
	for _, spec := range current.Specs {
		if guardEpochAdds(current.CodeEpoch, spec.Key.Relation) {
			additions = append(additions, spec)
			continue
		}
		predecessorTables = append(predecessorTables, spec.Key.Relation)
	}
	predecessor, err := buildGuardManifestAtEpoch(predecessorTables, current.CodeEpoch-1)
	if err != nil {
		return guardManifestEdge{}, false, err
	}
	if err := requireCompleteGuardEdition(predecessor); err != nil {
		return guardManifestEdge{}, false, fmt.Errorf(
			"sqlstore: guard edition %d cannot derive its complete epoch-%d predecessor: %w",
			current.CodeEpoch, predecessor.CodeEpoch, err)
	}

	// Carry-forward is byte identity, not merely key identity. Rebuilding from the same
	// constructor should make this tautological; keeping the check here makes a future
	// epoch-specific policy change fail at construction rather than silently authorizing
	// a changed old entry.
	for _, old := range predecessor.Specs {
		now, ok := current.lookup(old.Key)
		if !ok {
			return guardManifestEdge{}, false, fmt.Errorf(
				"sqlstore: guard edition %d drops predecessor entry %s without a retention transition",
				current.CodeEpoch, old.Key)
		}
		if !guardSpecsByteIdentical(old, now) {
			return guardManifestEdge{}, false, fmt.Errorf(
				"sqlstore: guard edition %d changes predecessor entry %s instead of carrying it forward byte-identically",
				current.CodeEpoch, old.Key)
		}
	}

	return guardManifestEdge{From: predecessor, To: current, Additions: additions}, true, nil
}

func requireCompleteGuardCurrentEdition(current guardManifest) error {
	if current.CodeEpoch < 2 || current.CodeEpoch > guardCodeEpoch {
		return fmt.Errorf("sqlstore: guard edition %d is not a current edition known to this binary", current.CodeEpoch)
	}
	return requireCompleteGuardEdition(current)
}

func requireCompleteGuardEdition(current guardManifest) error {
	if current.CodeEpoch == 1 {
		return nil
	}
	var missing []string
	for epoch := int64(2); epoch <= current.CodeEpoch; epoch++ {
		missing = append(missing, missingGuardTablesForEpoch(current, epoch)...)
	}
	if len(missing) != 0 {
		return fmt.Errorf("sqlstore: guard edition %d is incomplete: the closed registry is missing required epoch-%d relations %v; refusing to bootstrap or open this edition because adding them later under the same epoch would be manifest drift",
			current.CodeEpoch, current.CodeEpoch, missing)
	}
	if _, ok, err := guardManifestEditionEdge(current); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("sqlstore: guard edition %d has no complete compiled predecessor edge", current.CodeEpoch)
	}
	return nil
}

func missingGuardTablesForEpoch(current guardManifest, epoch int64) []string {
	var required []string
	switch epoch {
	case 2:
		required = []string{
			guardEpoch2DirectoryTombstoneTable,
			guardEpoch2UserTombstoneTable,
		}
	case 3:
		required = guardEpoch3CommunicationTables[:]
	case 4:
		required = guardEpoch4ProtocolTables[:]
	default:
		return nil
	}
	present := make(map[string]bool, len(required))
	for _, spec := range current.Specs {
		if guardEpochAdds(epoch, spec.Key.Relation) {
			present[spec.Key.Relation] = true
		}
	}
	missing := make([]string, 0, len(required))
	for _, table := range required {
		if !present[table] {
			missing = append(missing, table)
		}
	}
	return missing
}

func guardEpoch2Adds(table string) bool {
	return table == guardEpoch2UserTombstoneTable || table == guardEpoch2DirectoryTombstoneTable
}

func guardEpoch3Adds(table string) bool {
	for _, required := range guardEpoch3CommunicationTables {
		if table == required {
			return true
		}
	}
	return false
}

func guardEpoch4Adds(table string) bool {
	for _, required := range guardEpoch4ProtocolTables {
		if table == required {
			return true
		}
	}
	return false
}

func guardEpochAdds(epoch int64, table string) bool {
	switch epoch {
	case 2:
		return guardEpoch2Adds(table)
	case 3:
		return guardEpoch3Adds(table)
	case 4:
		return guardEpoch4Adds(table)
	default:
		return false
	}
}

func guardSpecsByteIdentical(a, b guardSpec) bool {
	return a.Key == b.Key &&
		a.Producer == b.Producer &&
		a.DefinitionSHA256 == b.DefinitionSHA256 &&
		a.SpecSHA256 == b.SpecSHA256 &&
		a.DesiredEnableState == b.DesiredEnableState &&
		equalStringSlices(a.LegacyAllowedStates, b.LegacyAllowedStates)
}

// functionDigest is the shared function's own footprint, used only to refuse two
// producers declaring it differently.
func functionDigest(f guardFunctionForm) ([32]byte, error) {
	w := newCanonWriter(canonDomainDefinition, guardManifestFormat)
	w.str("function-only")
	f.canon(w)
	return w.sum()
}

// guardUnitID is the stable identity of ONE unit: an entry plus the intent applied to it.
//
// It is a digest rather than a readable composite because it is a database key and a
// comparison target. A composite would have to choose a separator, and every separator
// this repository has tried has been a legal character in something it was separating —
// the dotted relation name, the comma-joined table list, the fixed dollar-quote tag. The
// digest is over the same length-prefixed canonical encoding as everything else, so its
// injectivity is a property of the encoding rather than of a lucky choice of delimiter.
func guardUnitID(format int64, k guardKey, intent unitIntent) (string, error) {
	if strings.TrimSpace(string(intent)) == "" {
		return "", fmt.Errorf("sqlstore: a guard unit id needs an intent")
	}
	w := newCanonWriter(canonDomainEntry, format)
	w.str("unit")
	k.canon(w)
	w.str(string(intent))
	d, err := w.sum()
	if err != nil {
		return "", err
	}
	return hexDigest(d), nil
}

// guardRolloutID is the identity of one rollout: the edition's target pair plus the
// retained pair the database was at when it opened.
//
// Deriving it rather than generating a random value is what makes a second boot able to
// recognize the SAME rollout instead of opening a duplicate one. Two boots of the same
// binary against the same database history compute the same id; a different edition, or a
// different retained history, computes a different one.
func guardRolloutID(format, codeEpoch int64, codeSHA [32]byte, retainedRevision int64, retainedSHA [32]byte) (string, error) {
	w := newCanonWriter(canonDomainManifest, format)
	w.str("rollout")
	w.i64(codeEpoch)
	w.bytes32(codeSHA)
	w.i64(retainedRevision)
	w.bytes32(retainedSHA)
	d, err := w.sum()
	if err != nil {
		return "", err
	}
	return hexDigest(d), nil
}

// emptyRetainedDigest is retained_sha256 for a database with no retained history: the
// fingerprint of the EMPTY canonical stream.
//
// A zero digest would be wrong in a way that matters. Zero is also what an uninitialised
// column, a failed scan or a truncated value reads as, so "no history" and "I could not
// read the history" would be the same bytes — and the whole point of the retained pair is
// to notice when history has gone missing.
func emptyRetainedDigest() ([32]byte, error) {
	w := newCanonWriter(canonDomainManifest, guardManifestFormat)
	w.str("retained-stream")
	w.i64(0)
	w.list(0)
	return w.sum()
}

// postgresMajorSupported reports whether this manifest's catalog projection has been
// reasoned about on that major.
func postgresMajorSupported(major int) bool {
	return major >= supportedPostgresMajorMin && major <= supportedPostgresMajorMax
}

// certifiedPostgresMajors are the majors whose deparser has been RUN against the declared
// shape, with the CHECK predicate text compared.
//
// THE LIST RECORDS A RUN, not an intent — and not a re-run either, which is the correction round
// five forced on the sentence that used to stand here ("THE LIST IS EVIDENCE"). Each entry means
// TestPostgresTheDeclaredShapeIsTheShapeEveryMajorCreates applied this binary's own control-plane
// DDL on a server of that major, projected the three relations back out of its catalog and
// compared them WITH the predicate text — the layer production had been running on one major
// only. What the entry does NOT mean is that any given run re-checked it: the matrix test SKIPS
// when OLIVARES_TEST_POSTGRES_MAJOR_DSNS is unset. That variable IS exported now — pg-majors.yml
// builds all four DSNs and hands them to every pass — so the sentence that said no workflow set it
// is retired; what remains true is that the LOCAL gate does not, and until a green pg-majors run
// exists on the exact SHA,
// this list is a dated measurement that a reader must trust, and widening it without re-running
// the matrix would be exactly the overclaim it was written to end. Measured 2026-07-30 on
// 15.18, 16.14, 17.10 and 18.4, and re-measured 2026-07-31 on the same four:
//
//	GUARD_SHAPE_CERTIFIED|major=16|relations=3|predicates_compared=true
//	GUARD_SHAPE_CERTIFIED|major=17|relations=3|predicates_compared=true
//	GUARD_SHAPE_CERTIFIED|major=18|relations=3|predicates_compared=true
//
// AND CERTIFYING 18 FOUND A REAL DEFECT, which is the argument for doing this rather than
// declaring a limit: PostgreSQL 18 materializes NOT NULL as a cataloged constraint
// (contype 'n'), 16 and 17 do not, and the projector rejected every unknown contype — so the
// engine would have REFUSED TO BOOT on 18 while calling it supported.
//
// A major in the supported range but NOT here still boots: the structural and literal layers
// run everywhere, and only the deparsed text is held back. That path is why widening the range
// before certifying is survivable rather than a brick — and why this list, not the range, is
// what turns the strongest layer on.
func certifiedPostgresMajors() []int { return []int{15, 16, 17, 18} }

// postgresMajorCertified reports whether the predicate text may be compared on this major.
func postgresMajorCertified(major int) bool {
	for _, m := range certifiedPostgresMajors() {
		if m == major {
			return true
		}
	}
	return false
}
