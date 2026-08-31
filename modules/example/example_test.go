// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package example_test

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"testing"
	"time"

	connexample "github.com/olivaresai/olivares/connectors/example"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/example"
	"github.com/olivaresai/olivares/sdk"
)

// countingRegistry is a fake ExtensionRegistry that records how many entities a
// module declares, so the schema seam can be exercised without a real store.
type countingRegistry struct {
	registered       int
	schemaInvariants []string
}

var _ store.ExtensionRegistry = (*countingRegistry)(nil)

func (r *countingRegistry) Register(model.EntityDescriptor) error { r.registered++; return nil }
func (r *countingRegistry) Migrations(string, fs.FS) error        { return nil }

// SchemaInvariants RECORDS the declaration, and the previous version of this fake
// returned nil while discarding it. That is the failure the method is on the
// interface to prevent, reproduced one layer down: a module could declare its
// security invariants along a tested registration path and the test would see
// nothing, report success, and prove the opposite of what it looks like it proves.
// A compiler proves the method is present; only recording proves it was heard.
func (r *countingRegistry) SchemaInvariants(ns string, byEngine map[store.Engine][]store.SchemaTrigger) error {
	r.schemaInvariants = append(r.schemaInvariants, ns)
	_ = byEngine
	return nil
}

func (r *countingRegistry) WorkspaceInitializer(store.WorkspaceInitializer) error { return nil }

// RolloutControl accepts a staged-control declaration (unit G). This fake records
// nothing about it because the code under test declares none; the engine's own registry
// is where a declaration is validated and classified.
func (r *countingRegistry) RolloutControl(store.RolloutControl) error { return nil }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestExampleConnectorAndModuleCommunicate is the S02 Definition-of-Done proof:
// the reference source connector and the reference module are loaded by the
// runtime and communicate through the event bus. The connector emits N edges;
// the module, subscribed via its Host, counts exactly N.
func TestExampleConnectorAndModuleCommunicate(t *testing.T) {
	const want = 5

	rt := runtime.New(runtime.Options{Logger: quiet()})

	src := connexample.New()
	mod := example.New()

	if err := rt.AddSource(src, sdk.Config{Settings: map[string]string{
		"count":    "5",
		"resource": "public.orders",
	}}, "tenant-demo"); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	// Delivery is asynchronous; poll until the module has counted all edges.
	deadline := time.After(3 * time.Second)
	for mod.Count() < want {
		select {
		case <-deadline:
			t.Fatalf("module counted %d edges, want %d", mod.Count(), want)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := mod.Count(); got != want {
		t.Fatalf("module counted %d edges, want exactly %d", got, want)
	}
}

// TestExampleModuleImplementsSchemaProvider proves the reference module wires
// into the engine-side schema seam (it is the structural match the runtime
// probes; here we assert it directly so a future signature drift is caught).
func TestExampleModuleImplementsSchemaProvider(t *testing.T) {
	var _ runtime.SchemaProvider = example.New()
	// Drive RegisterSchema through the runtime against the real registry-backed
	// store to confirm the demo entity actually builds.
	rt := runtime.New(runtime.Options{Logger: quiet()})
	if err := rt.AddModule(example.New(), sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	reg := &countingRegistry{}
	if err := rt.RegisterSchema(reg); err != nil {
		t.Fatalf("RegisterSchema: %v", err)
	}
	if len(reg.schemaInvariants) != 0 {
		t.Errorf("this module declared schema invariants %v: the fake now RECORDS them "+
			"instead of discarding them, so this line is the decision point — assert what "+
			"they are, do not just raise the number", reg.schemaInvariants)
	}
	if reg.registered != 1 {
		t.Errorf("expected the example module to register 1 entity, got %d", reg.registered)
	}
}
