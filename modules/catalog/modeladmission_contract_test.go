// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

// Root-cause guard for the DECOUPLED cross-module read. modeladmission.go reads the models
// module's models.admission_policy and models.model_admission entities BY KIND + COLUMN NAME via
// sc.Ext — deliberately, so the catalog never imports the models module at runtime (the license /
// layering boundary; check-boundary.sh). That loose coupling has one failure mode: if the models
// module renames or drops a column the catalog still reads, nothing breaks at compile time — the
// reconstructed trust policy silently loses an anchor, or a verdict field reads empty, and the
// deny-closed gate SKIPS a check instead of erroring (fail-OPEN). The runtime schema-drift tripwire
// in modelAdmissionRefusal is the belt against that; this test is the suspenders.
//
// It makes the implicit contract explicit: it registers the models module's REAL schema (a
// TEST-only import — it never enters the shipped catalog binary, so the runtime boundary stands)
// and asserts every column name the catalog hard-codes exists on the models entity it targets. A
// rename in the models module breaks THIS test, at the source, instead of quietly weakening the
// admission gate in production.

import (
	"io/fs"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

// captureRegistry records the EntityDescriptors a module's RegisterSchema declares, so a test can
// introspect the ACTUAL columns a sibling module exposes without standing up a full engine.
type captureRegistry struct {
	descs            map[model.Kind]model.EntityDescriptor
	schemaInvariants []string
}

func (c *captureRegistry) Register(d model.EntityDescriptor) error { c.descs[d.Kind] = d; return nil }
func (c *captureRegistry) Migrations(string, fs.FS) error          { return nil }

// SchemaInvariants RECORDS the declaration, and the previous version of this fake
// returned nil while discarding it. That is the failure the method is on the
// interface to prevent, reproduced one layer down: a module could declare its
// security invariants along a tested registration path and the test would see
// nothing, report success, and prove the opposite of what it looks like it proves.
// A compiler proves the method is present; only recording proves it was heard.
func (c *captureRegistry) SchemaInvariants(ns string, byEngine map[store.Engine][]store.SchemaTrigger) error {
	c.schemaInvariants = append(c.schemaInvariants, ns)
	_ = byEngine
	return nil
}

func (c *captureRegistry) WorkspaceInitializer(store.WorkspaceInitializer) error { return nil }

// RolloutControl accepts a staged-control declaration (unit G). This fake records
// nothing about it because the code under test declares none; the engine's own registry
// is where a declaration is validated and classified.
func (c *captureRegistry) RolloutControl(store.RolloutControl) error { return nil }

func TestModelAdmissionColumnContract(t *testing.T) {
	reg := &captureRegistry{descs: map[model.Kind]model.EntityDescriptor{}}
	if err := models.New().RegisterSchema(reg); err != nil {
		t.Fatalf("register models schema: %v", err)
	}

	if len(reg.schemaInvariants) != 0 {
		t.Errorf("this module declared schema invariants %v: the fake now RECORDS them "+
			"instead of discarding them, so this line is the decision point — assert what "+
			"they are, do not just raise the number", reg.schemaInvariants)
	}
	columnsOf := func(kind model.Kind) map[string]bool {
		d, ok := reg.descs[kind]
		if !ok {
			t.Fatalf("models module no longer registers %q — the catalog's decoupled read target is gone", kind)
		}
		cols := map[string]bool{}
		for _, f := range d.Fields {
			cols[f.Name] = true
		}
		return cols
	}

	// The sc.Ext lookup keys themselves must match the models module's registered kinds.
	if _, ok := reg.descs[admissionPolicyExtKind]; !ok {
		t.Errorf("catalog reads kind %q which the models module does not register", admissionPolicyExtKind)
	}
	if _, ok := reg.descs[modelAdmissionExtKind]; !ok {
		t.Errorf("catalog reads kind %q which the models module does not register", modelAdmissionExtKind)
	}

	assert := func(kind model.Kind, cols map[string]bool, want map[string]string) {
		for constName, col := range want {
			if !cols[col] {
				t.Errorf("%s: catalog reads %s.%q (const %s) but the models schema has no such column — decoupled contract drift", kind, kind, col, constName)
			}
		}
	}

	// Every column the catalog reads from models.admission_policy (loadRequireSigned + the trust
	// policy reconstruction) must exist on that entity.
	assert(admissionPolicyExtKind, columnsOf(admissionPolicyExtKind), map[string]string{
		"colExtPolicyScope":     colExtPolicyScope,
		"colExtRequireSigned":   colExtRequireSigned,
		"colExtRequireArtifact": colExtRequireArtifact,
		"colExtPolIdentities":   colExtPolIdentities,
		"colExtPolIssuers":      colExtPolIssuers,
		"colExtPolKeys":         colExtPolKeys,
		"colExtPolRoots":        colExtPolRoots,
	})

	// Every column the catalog reads from models.model_admission (the verdict + its recorded anchor,
	// including the signer_roots pin) must exist on that entity.
	assert(modelAdmissionExtKind, columnsOf(modelAdmissionExtKind), map[string]string{
		"colExtAdmVersion":  colExtAdmVersion,
		"colExtAdmVerified": colExtAdmVerified,
		"colExtAdmArtifact": colExtAdmArtifact,
		"colExtAdmReason":   colExtAdmReason,
		"colExtAdmMethod":   colExtAdmMethod,
		"colExtAdmIdentity": colExtAdmIdentity,
		"colExtAdmIssuer":   colExtAdmIssuer,
		"colExtAdmRoots":    colExtAdmRoots,
	})
}
