// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestProtocolBindingStorageDeclarationsAreComplete(t *testing.T) {
	t.Parallel()

	reg := &workSchemaCaptureRegistry{}
	if err := New().registerProtocolBindingSchema(reg); err != nil {
		t.Fatalf("register protocol binding schema: %v", err)
	}
	retained := map[model.Kind]bool{
		protocolBindingSpecKind: false,
		protocolBindingKind:     false,
	}
	for _, descriptor := range reg.descriptors {
		if _, ok := retained[descriptor.Kind]; !ok {
			continue
		}
		if descriptor.AppendOnly || !descriptor.RetainOnTenantDrop {
			t.Errorf("descriptor %s append_only=%t retain_on_tenant_drop=%t", descriptor.Kind, descriptor.AppendOnly, descriptor.RetainOnTenantDrop)
		}
		retained[descriptor.Kind] = true
	}
	for kind, found := range retained {
		if !found {
			t.Errorf("descriptor %s was not registered", kind)
		}
	}

	want := map[store.Engine]map[string]string{
		store.EnginePostgres: {
			"sessions_communication_binding_spec_guard":     protocolBindingSpecTable,
			"sessions_communication_binding_guard":          protocolBindingTable,
			"sessions_communication_binding_spec_no_delete": protocolBindingSpecTable,
			"sessions_communication_binding_no_delete":      protocolBindingTable,
		},
		store.EngineSQLite: {
			"sessions_communication_binding_spec_guard_ins": protocolBindingSpecTable,
			"sessions_communication_binding_spec_guard_upd": protocolBindingSpecTable,
			"sessions_communication_binding_guard_ins":      protocolBindingTable,
			"sessions_communication_binding_guard_upd":      protocolBindingTable,
			"sessions_communication_binding_spec_no_delete": protocolBindingSpecTable,
			"sessions_communication_binding_no_delete":      protocolBindingTable,
		},
	}
	for engineName, invariants := range protocolBindingSchemaInvariants() {
		seen := make(map[string]bool, len(invariants))
		for _, invariant := range invariants {
			if invariant.Table != want[engineName][invariant.Name] {
				t.Errorf("%s invariant %s targets %s, want %s", engineName, invariant.Name, invariant.Table, want[engineName][invariant.Name])
			}
			if len(invariant.DefinitionSHA256) != sha256.Size*2 {
				t.Errorf("%s invariant %s has invalid digest %q", engineName, invariant.Name, invariant.DefinitionSHA256)
			}
			seen[invariant.Name] = true
		}
		if len(seen) != len(want[engineName]) {
			t.Errorf("%s invariant set = %v, want %v", engineName, seen, want[engineName])
		}
	}
}

type protocolBindingCatalogRegistry struct {
	store.ExtensionRegistry
}

func (r protocolBindingCatalogRegistry) Register(descriptor model.EntityDescriptor) error {
	if descriptor.Kind == protocolBindingSpecKind || descriptor.Kind == protocolBindingKind {
		descriptor.RetainOnTenantDrop = false
	}
	return r.ExtensionRegistry.Register(descriptor)
}

func protocolBindingCatalogSchema(reg store.ExtensionRegistry) error {
	return New().RegisterSchema(protocolBindingCatalogRegistry{ExtensionRegistry: reg})
}

func protocolBindingDeclaredDigest(engineName store.Engine, triggerName string) string {
	for _, invariant := range protocolBindingSchemaInvariants()[engineName] {
		if invariant.Name == triggerName {
			return invariant.DefinitionSHA256
		}
	}
	return ""
}

func protocolBindingAssertCatalogDigest(
	t *testing.T,
	engineName store.Engine,
	triggerName, definition string,
) {
	t.Helper()
	sum := sha256.Sum256([]byte(definition))
	got := hex.EncodeToString(sum[:])
	t.Logf("%s %s %s", engineName, triggerName, got)
	if want := protocolBindingDeclaredDigest(engineName, triggerName); want != "" && got != want {
		t.Errorf("%s %s catalog digest = %s, declared %s", engineName, triggerName, got, want)
	}
}

func TestProtocolBindingStorageCatalogDigests(t *testing.T) {
	t.Parallel()

	names := map[store.Engine][]string{
		store.EngineSQLite: {
			"sessions_communication_binding_spec_guard_ins",
			"sessions_communication_binding_spec_guard_upd",
			"sessions_communication_binding_guard_ins",
			"sessions_communication_binding_guard_upd",
			"sessions_communication_binding_spec_no_delete",
			"sessions_communication_binding_no_delete",
		},
		store.EnginePostgres: {
			"sessions_communication_binding_spec_guard",
			"sessions_communication_binding_guard",
			"sessions_communication_binding_spec_no_delete",
			"sessions_communication_binding_no_delete",
		},
	}

	t.Run("sqlite", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "protocol-binding-catalog.db")
		st, err := engine.Open(context.Background(), store.Config{
			Engine: store.EngineSQLite, DSN: dsn, Debug: true,
		}, protocolBindingCatalogSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close() //nolint:errcheck
		for _, name := range names[store.EngineSQLite] {
			var definition string
			if err := raw.QueryRow(
				"SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?", name,
			).Scan(&definition); err != nil {
				t.Fatal(err)
			}
			protocolBindingAssertCatalogDigest(t, store.EngineSQLite, name, definition)
		}
	})

	if !enginetest.PostgresAvailable(t) {
		t.Logf("%s unset: PostgreSQL ProtocolBinding catalog not exercised", enginetest.EnvSuperuserDSN)
		return
	}
	t.Run("postgres", func(t *testing.T) {
		pg := enginetest.IsolatedPostgres(t)
		st, err := engine.Open(context.Background(), store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, Debug: true,
		}, protocolBindingCatalogSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := sql.Open("pgx", pg.App)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close() //nolint:errcheck
		for _, name := range names[store.EnginePostgres] {
			var triggerDefinition, functionDefinition string
			if err := raw.QueryRow(`
SELECT pg_catalog.pg_get_triggerdef(t.oid, false), pg_catalog.pg_get_functiondef(t.tgfoid)
FROM pg_catalog.pg_trigger t
WHERE t.tgname = $1 AND NOT t.tgisinternal`, name).Scan(
				&triggerDefinition, &functionDefinition,
			); err != nil {
				t.Fatal(err)
			}
			definition := fmt.Sprintf(
				"trigger:%d:%sfunction:%d:%s",
				len(triggerDefinition), triggerDefinition,
				len(functionDefinition), functionDefinition,
			)
			protocolBindingAssertCatalogDigest(t, store.EnginePostgres, name, definition)
		}
	})
}

func TestProtocolBindingStorageGuardsLifecycleAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			f := workFixtureForBackend(t, m, tenant)
			work := addWorkLeaseDomainItem(t, f, "protocol storage guards "+be.name)
			active := applyProtocolSpecForTest(t, m, tenant,
				protocolSpecInputForTest(f.workspace, BindingProtocolA2A, "storage-guards-"+be.name, 1, model.ID("")))
			if err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(protocolBindingSpecKind)
				if err != nil {
					return err
				}
				record, err := repo.Get(context.Background(), active.ID)
				if err != nil {
					return err
				}
				record[colBindingRemoteResourceRef] = "agent:rewritten"
				_, err = repo.Update(context.Background(), record)
				return err
			}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "protocolbindingspec") {
				t.Fatalf("direct spec rewrite error = %v, want immutable-config rejection", err)
			}
			reservation := ProtocolBindingReservation{
				WorkspaceID: f.workspace, BindingSpecID: active.ID,
				BindingSpecGeneration: active.Generation, ExpectedDirection: BindingOutbound,
				WorkItemID: work.ready.ResultID, DispatchKey: "dispatch:storage-guards:" + be.name,
				ExpectedExternalKind: string(ProtocolBindingResultTask), Generation: 1,
				OwnerKind: "agent", OwnerRef: work.agentRef, OwnerEpoch: 1,
			}
			binding, err := m.ReserveProtocolBinding(context.Background(), tenant, reservation)
			if err != nil {
				t.Fatalf("reserve protocol binding: %v", err)
			}

			reject := func(label string, mutate func(store.GenericRepo, model.Record) error) {
				t.Helper()
				err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(protocolBindingKind)
					if err != nil {
						return err
					}
					record, err := repo.Get(context.Background(), binding.ID)
					if err != nil {
						return err
					}
					return mutate(repo, record)
				})
				message := strings.ToLower(fmt.Sprint(err))
				if err == nil || (!strings.Contains(message, "protocolbinding") && !strings.Contains(message, "protocol binding")) {
					t.Fatalf("%s error = %v, want storage guard rejection", label, err)
				}
			}
			reject("pinned spec drift", func(repo store.GenericRepo, record model.Record) error {
				record[colBindingPinnedSpecHash] = bytes.Repeat([]byte{0x7f}, sha256.Size)
				_, err := repo.Update(context.Background(), record)
				return err
			})
			reject("hard delete", func(repo store.GenericRepo, _ model.Record) error {
				return repo.Delete(context.Background(), binding.ID)
			})

			settled, err := m.SettleProtocolBinding(context.Background(), tenant, ProtocolBindingSettlement{
				BindingID: binding.ID, Generation: binding.Generation, ExpectedVersion: binding.Version,
				DispatchKey: reservation.DispatchKey, ResultKind: ProtocolBindingResultTask,
				ExternalID: "task:storage-guards:" + be.name, ContextID: "context:storage-guards:" + be.name,
				LocalState: "review", RemoteState: "completed", RemoteRevision: "rev:terminal",
				Verdict: ProtocolObservationClean, Code: "task_completed", Observed: true, Terminal: true,
			})
			if err != nil || !settled.Terminal {
				t.Fatalf("terminal settlement = %#v, %v", settled, err)
			}
			binding = settled
			reject("terminal rewrite", func(repo store.GenericRepo, record model.Record) error {
				record[colBindingRemoteState] = "working"
				_, err := repo.Update(context.Background(), record)
				return err
			})

			for _, kindID := range []struct {
				kind model.Kind
				id   model.ID
			}{{protocolBindingSpecKind, active.ID}, {protocolBindingKind, binding.ID}} {
				err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(kindID.kind)
					if err != nil {
						return err
					}
					return repo.Delete(context.Background(), kindID.id)
				})
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "deleted") {
					t.Fatalf("delete %s error = %v, want no-delete rejection", kindID.kind, err)
				}
			}
		})
	}
}
