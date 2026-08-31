// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sort"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// The schema manifest is the open≡enterprise schema-PARITY oracle (§3 point 5).
// It serializes the EXACT schema this binary registers at boot — every module's
// entity descriptors (tables, columns, indexes, guards) plus its file migrations
// (per-engine .sql, by content hash) — deterministically, so the same module set
// produces byte-identical output. This `migrate manifest` command stays in the open
// build; the open≡enterprise parity gate that builds cmd/olivares BOTH ways (default and
// -tags enterprise) and diffs `migrate manifest` runs in the separate, private enterprise
// distribution — the only tree that can build both binaries. Equal output proves a
// binary swap open↔enterprise never lands in a partial-upgrade schema state.
//
// Today both tags register the SAME schema (enterprise adds CAPABILITY to existing
// modules, never its own tables — verified: no .sql under enterprise/, no enterprise
// RegisterSchema). The gate is the REGRESSION guard for when (WORM) /
// (regulatory) want enterprise rows: they MUST go through the shared module chain, so
// both builds carry them — never a tag-gated fork.

type schemaManifest struct {
	SchemaVersion int                 `json:"schema_version"`
	Entities      []manifestEntity    `json:"entities"`
	Migrations    []manifestMigration `json:"migrations"`
	Invariants    []manifestInvariant `json:"invariants,omitempty"`
	// RolloutControls records the staged controls modules declare (unit G).
	// A control belongs in the manifest for the same reason a table does: the
	// open↔enterprise parity gate proves a binary swap never lands in a partial
	// schema state, and a control the two builds disagreed about would mean one of
	// them classifies a deployment the other does not.
	RolloutControls []manifestRolloutControl `json:"rollout_controls,omitempty"`
	// WorkspaceInitializers are executable bootstrap declarations rather than
	// schema objects, but a binary swap that drops one changes the state a newly
	// created workspace receives. The parity oracle must therefore record them.
	WorkspaceInitializers []manifestWorkspaceInitializer `json:"workspace_initializers,omitempty"`
}

type manifestEntity struct {
	Kind               string          `json:"kind"`
	Table              string          `json:"table"`
	Fields             []manifestField `json:"fields"`
	Indexes            []manifestIndex `json:"indexes,omitempty"`
	Audited            bool            `json:"audited,omitempty"`
	AppendOnly         bool            `json:"append_only,omitempty"`
	RetainOnTenantDrop bool            `json:"retain_on_tenant_drop,omitempty"`
	SoftDelete         bool            `json:"soft_delete,omitempty"`
}

type manifestField struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Nullable bool   `json:"nullable,omitempty"`
	Indexed  bool   `json:"indexed,omitempty"`
	Redact   bool   `json:"redact,omitempty"`
}

type manifestIndex struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
}

type manifestMigration struct {
	Namespace string                `json:"namespace"`
	Files     []manifestMigrateFile `json:"files"`
}

// manifestInvariant records a module's declared per-engine security triggers. They
// belong in the parity oracle for the same reason the entity guards do: they are
// schema the store REFUSES to boot without, so an open/enterprise pair that declared
// different invariants would be a partial-upgrade hazard the manifest must catch.
type manifestInvariant struct {
	Namespace string                       `json:"namespace"`
	ByEngine  map[string][]manifestTrigger `json:"by_engine"`
}

type manifestTrigger struct {
	Name  string `json:"name"`
	Table string `json:"table"`
	// DefinitionSHA256 must travel with the declaration: the store refuses to open
	// when a live trigger's catalog text does not hash to it, so a manifest that
	// omitted it would report two builds identical while they enforced different
	// bodies.
	DefinitionSHA256 string `json:"definition_sha256,omitempty"`
}

type manifestMigrateFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type manifestRolloutControl struct {
	Key          string `json:"key"`
	WitnessTable string `json:"witness_table"`
	LegacyMode   string `json:"legacy_mode"`
	FreshMode    string `json:"fresh_mode"`
}

type manifestWorkspaceInitializer struct {
	Key string `json:"key"`
}

// schemaManifestRegistry implements store.ExtensionRegistry by RECORDING every schema
// declaration instead of building SQL — so the registration path that runs at boot
// (rt.RegisterSchema) yields a comparable manifest with no database.
type schemaManifestRegistry struct {
	entities     []manifestEntity
	migrations   []manifestMigration
	invariants   []manifestInvariant
	rollout      []manifestRolloutControl
	initializers []manifestWorkspaceInitializer
}

// Compile-time proof the recorder satisfies the same seam the store hands modules.
var _ store.ExtensionRegistry = (*schemaManifestRegistry)(nil)

func (r *schemaManifestRegistry) Register(d model.EntityDescriptor) error {
	e := manifestEntity{
		Kind: string(d.Kind), Table: d.Table,
		Audited: d.Audited, AppendOnly: d.AppendOnly,
		RetainOnTenantDrop: d.RetainOnTenantDrop, SoftDelete: d.SoftDelete,
	}
	for _, f := range d.Fields {
		e.Fields = append(e.Fields, manifestField{
			Name: f.Name, Kind: f.Kind.String(), Nullable: f.Nullable, Indexed: f.Indexed, Redact: f.Redact,
		})
	}
	for _, ix := range d.Indexes {
		e.Indexes = append(e.Indexes, manifestIndex{
			Name: ix.Name, Columns: append([]string(nil), ix.Columns...), Unique: ix.Unique,
		})
	}
	r.entities = append(r.entities, e)
	return nil
}

func (r *schemaManifestRegistry) RolloutControl(c store.RolloutControl) error {
	// Validated here as well as in the engine, because this recorder is the path the
	// parity gate exercises and it never opens a store: an illegal declaration that
	// only the engine rejected would produce a clean manifest and a failing boot.
	if err := c.Validate(); err != nil {
		return err
	}
	r.rollout = append(r.rollout, manifestRolloutControl{
		Key: c.Key, WitnessTable: c.WitnessTable,
		LegacyMode: string(c.LegacyMode), FreshMode: string(c.FreshMode),
	})
	return nil
}

func (r *schemaManifestRegistry) WorkspaceInitializer(i store.WorkspaceInitializer) error {
	if err := i.Validate(); err != nil {
		return err
	}
	for _, existing := range r.initializers {
		if existing.Key == i.Key {
			return fmt.Errorf("%w: duplicate workspace initializer key %q", store.ErrInvalidDescriptor, i.Key)
		}
	}
	r.initializers = append(r.initializers, manifestWorkspaceInitializer{Key: i.Key})
	return nil
}

func (r *schemaManifestRegistry) Migrations(namespace string, fsys fs.FS) error {
	m := manifestMigration{Namespace: namespace}
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(b)
		m.Files = append(m.Files, manifestMigrateFile{Path: path, SHA256: hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk migrations for %q: %w", namespace, err)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	r.migrations = append(r.migrations, m)
	return nil
}

// SchemaInvariants records the declaration rather than dropping it. A no-op here
// would reintroduce, in the parity oracle, exactly the silent hole that moving
// SchemaInvariants into ExtensionRegistry closed: the manifest would report two
// builds identical while they required different security triggers.
func (r *schemaManifestRegistry) SchemaInvariants(
	namespace string,
	byEngine map[store.Engine][]store.SchemaTrigger,
) error {
	inv := manifestInvariant{Namespace: namespace, ByEngine: map[string][]manifestTrigger{}}
	for engine, triggers := range byEngine {
		recorded := make([]manifestTrigger, 0, len(triggers))
		for _, t := range triggers {
			recorded = append(recorded, manifestTrigger{
				Name: t.Name, Table: t.Table, DefinitionSHA256: t.DefinitionSHA256,
			})
		}
		// Deterministic within an engine; map iteration order never reaches JSON
		// because encoding/json sorts map keys.
		sort.Slice(recorded, func(i, j int) bool {
			if recorded[i].Name != recorded[j].Name {
				return recorded[i].Name < recorded[j].Name
			}
			return recorded[i].Table < recorded[j].Table
		})
		inv.ByEngine[string(engine)] = recorded
	}
	r.invariants = append(r.invariants, inv)
	return nil
}

// collectSchemaManifest builds the SAME module set boot wires and runs its schema
// registration into the recorder — no DB, no network. The signing keys are throwaway
// fixed seeds: the schema descriptors are static and never depend on a key value, so
// the manifest is fully deterministic and key-independent. The inference Doer is nil
// (modules store it; nothing calls it at construction). Output is canonicalized
// (entities by table, migrations by namespace) so registration order can never cause
// a spurious diff.
func collectSchemaManifest() (*schemaManifest, error) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	signer, err := audit.NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		return nil, fmt.Errorf("schema manifest: signer: %w", err)
	}
	catalogPriv := ed25519.NewKeyFromSeed(fixedSeed(1))
	policyPriv := ed25519.NewKeyFromSeed(fixedSeed(2))

	set, err := buildModules(signer, catalogPriv, policyPriv, nil, nil, sourcesConfig{}, log)
	if err != nil {
		return nil, fmt.Errorf("schema manifest: load module operator config: %w", err)
	}
	rt := runtime.New(runtime.Options{Logger: log})
	for _, m := range set.all {
		sm, ok := m.(sdk.Module)
		if !ok {
			return nil, fmt.Errorf("schema manifest: module %q does not satisfy sdk.Module", m.APINamespace())
		}
		if aerr := rt.AddModule(sm, sdk.Config{}); aerr != nil {
			return nil, fmt.Errorf("schema manifest: register module %q: %w", m.APINamespace(), aerr)
		}
	}
	reg := &schemaManifestRegistry{}
	if rerr := rt.RegisterSchema(reg); rerr != nil {
		return nil, fmt.Errorf("schema manifest: register schema: %w", rerr)
	}
	sort.Slice(reg.entities, func(i, j int) bool { return reg.entities[i].Table < reg.entities[j].Table })
	sort.Slice(reg.migrations, func(i, j int) bool { return reg.migrations[i].Namespace < reg.migrations[j].Namespace })
	sort.Slice(reg.invariants, func(i, j int) bool { return reg.invariants[i].Namespace < reg.invariants[j].Namespace })
	sort.Slice(reg.rollout, func(i, j int) bool { return reg.rollout[i].Key < reg.rollout[j].Key })
	sort.Slice(reg.initializers, func(i, j int) bool { return reg.initializers[i].Key < reg.initializers[j].Key })
	// ONE return carrying BOTH sides' fields: the branch adds Invariants, main adds
	// RolloutControls, and the manifest is meant to describe everything the registry
	// recorded. Two returns left the second unreachable and the manifest silently
	// missing half of itself.
	return &schemaManifest{
		// Version 3 adds workspace initializers. Keeping version 2 while changing
		// the hashed registry shape would make a consumer interpret new parity
		// evidence under an older vocabulary.
		SchemaVersion:         3,
		Entities:              reg.entities,
		Migrations:            reg.migrations,
		Invariants:            reg.invariants,
		RolloutControls:       reg.rollout,
		WorkspaceInitializers: reg.initializers,
	}, nil
}

// fixedSeed returns a deterministic 32-byte seed (so the manifest never depends on
// process entropy). The value is irrelevant to the schema — only its determinism is.
func fixedSeed(b byte) []byte {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = b
	}
	return s
}

// migrateManifestCmd prints this binary's registered schema manifest plus a trailing
// sha256 digest — the open≡enterprise parity gate's oracle.
func migrateManifestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "manifest",
		Short: "Print this binary's registered schema manifest (deterministic; the open≡enterprise parity oracle)",
		Long: "manifest serializes the EXACT schema this binary registers at boot — every module's entity\n" +
			"descriptors (tables/columns/indexes/guards) and file migrations (by content hash) — as canonical\n" +
			"JSON plus a trailing sha256. It opens no database. The open≡enterprise parity gate runs this\n" +
			"command on BOTH artifacts — the community binary built here and the enterprise binary built from\n" +
			"its own distribution — and asserts identical output, so a binary swap between editions can never\n" +
			"land in a partial-upgrade schema state (docs/UPGRADE-AND-ROLLBACK.md).",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			man, err := collectSchemaManifest()
			if err != nil {
				return err
			}
			b, err := json.MarshalIndent(man, "", "  ")
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			fmt.Fprintln(cmd.OutOrStdout(), string(b))
			fmt.Fprintf(cmd.OutOrStdout(), "sha256:%s\n", hex.EncodeToString(sum[:]))
			return nil
		},
	}
}
