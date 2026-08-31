// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"io/fs"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRegisterSchemaDeclaresConsentAndAppendOnlyEvidence(t *testing.T) {
	reg := &recordingRegistry{}
	if err := New().RegisterSchema(reg); err != nil {
		t.Fatalf("RegisterSchema: %v", err)
	}
	if len(reg.schemaInvariants) != 0 {
		t.Errorf("this module declared schema invariants %v: the fake now RECORDS them "+
			"instead of discarding them, so this line is the decision point — assert what "+
			"they are, do not just raise the number", reg.schemaInvariants)
	}
	if len(reg.desc) != 3 {
		t.Fatalf("descriptors = %d, want 3", len(reg.desc))
	}
	if len(reg.rollout) != 0 {
		t.Fatalf("rollout controls = %d, want 0: a staged control classifies every deployment on this module's history, and nobody has decided that for redteam", len(reg.rollout))
	}
	target := reg.byKind(targetKind)
	if target.Table != targetTable || target.AppendOnly {
		t.Fatalf("target descriptor = %+v", target)
	}
	if len(target.Indexes) != 1 || !target.Indexes[0].Unique {
		t.Fatalf("target indexes = %+v, want unique tenant+agent_ref", target.Indexes)
	}
	if got := target.Indexes[0].Columns; len(got) != 2 || got[0] != model.ColTenantID || got[1] != colAgentRef {
		t.Fatalf("target unique columns = %+v", got)
	}
	for _, kind := range []model.Kind{runKind, resultKind} {
		d := reg.byKind(kind)
		if !d.AppendOnly {
			t.Fatalf("%s AppendOnly=false, want true", kind)
		}
		if len(d.Fields) == 0 {
			t.Fatalf("%s has no fields", kind)
		}
	}
}

type recordingRegistry struct {
	desc             []model.EntityDescriptor
	rollout          []store.RolloutControl
	schemaInvariants []string
}

func (r *recordingRegistry) Register(d model.EntityDescriptor) error {
	r.desc = append(r.desc, d)
	return nil
}

func (r *recordingRegistry) Migrations(string, fs.FS) error { return nil }

// SchemaInvariants RECORDS the declaration, and the previous version of this fake
// returned nil while discarding it. That is the failure the method is on the
// interface to prevent, reproduced one layer down: a module could declare its
// security invariants along a tested registration path and the test would see
// nothing, report success, and prove the opposite of what it looks like it proves.
// A compiler proves the method is present; only recording proves it was heard.
func (r *recordingRegistry) SchemaInvariants(ns string, byEngine map[store.Engine][]store.SchemaTrigger) error {
	r.schemaInvariants = append(r.schemaInvariants, ns)
	_ = byEngine
	return nil
}

func (r *recordingRegistry) WorkspaceInitializer(store.WorkspaceInitializer) error { return nil }

// RolloutControl records staged controls (unit G). This module declares none, and
// the assertion below says so rather than leaving it to the reader: a control declared
// here would classify every deployment on this module's history, which is a decision
// nobody has made for redteam.
func (r *recordingRegistry) RolloutControl(c store.RolloutControl) error {
	r.rollout = append(r.rollout, c)
	return nil
}

func (r *recordingRegistry) byKind(kind model.Kind) model.EntityDescriptor {
	for _, d := range r.desc {
		if d.Kind == kind {
			return d
		}
	}
	return model.EntityDescriptor{}
}
