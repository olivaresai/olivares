// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import "fmt"

// WorkspaceInheritedReadSpec declares the stable parent reference carried by
// an append-only child. ParentColumn is a tenant-unique parent key; Column is
// the child's matching reference. The owning module's producers must keep this
// reference stable for the parent's lifetime, as with a foreign key. It grants
// no collection reader, writer or recursive lineage traversal.
type WorkspaceInheritedReadSpec struct {
	ParentKind   Kind
	ParentColumn string
	Column       string
}

// Declared includes partial declarations so they are refused rather than ignored.
func (s WorkspaceInheritedReadSpec) Declared() bool {
	return s.ParentKind != "" || s.ParentColumn != "" || s.Column != ""
}

// ValidateInheritedWorkspaceRead admits only an append-only, one-level read
// relation within its owning module. Both registry admission and the reader
// validate it: an opaque Scope must not fabricate a broader relation at runtime.
func ValidateInheritedWorkspaceRead(child, parent EntityDescriptor) error {
	s := child.WorkspaceInheritedRead
	if !s.Declared() {
		return nil
	}
	if !child.AppendOnly || child.SoftDelete || child.WorkspaceConfinedReadOnly || child.WorkspaceLineage != (WorkspaceLineageSpec{}) {
		return fmt.Errorf("inherited read requires an append-only child without direct lineage")
	}
	if !s.ParentKind.Valid() || s.ParentKind != parent.Kind || s.ParentKind == child.Kind || parent.Kind.Namespace() != child.Kind.Namespace() {
		return fmt.Errorf("inherited read requires a distinct registered parent in the same namespace")
	}
	if !isIdent(s.ParentColumn) || !isIdent(s.Column) || IsReservedColumn(s.ParentColumn) || IsReservedColumn(s.Column) {
		return fmt.Errorf("inherited read requires explicit reference columns")
	}
	lineage := parent.WorkspaceLineage
	if !lineage.Declared() || !lineage.Encoding.ValidEncoding() || !lineage.Unset.ValidUnset() || parent.WorkspaceInheritedRead.Declared() {
		return fmt.Errorf("inherited read parent requires direct workspace lineage")
	}
	field := func(d EntityDescriptor, name string) (FieldSpec, bool) {
		for _, f := range d.Fields {
			if f.Name == name {
				return f, true
			}
		}
		return FieldSpec{}, false
	}
	pf, pok := field(parent, s.ParentColumn)
	cf, cok := field(child, s.Column)
	if !pok || !cok || pf.Nullable || cf.Nullable || pf.Redact || cf.Redact || pf.Kind != cf.Kind ||
		(pf.Kind != KindText && pf.Kind != KindUUID) {
		return fmt.Errorf("inherited read requires matching non-null, readable reference columns")
	}
	wf, wok := field(parent, lineage.Column)
	if !wok || wf.Redact || (wf.Kind != KindText && wf.Kind != KindUUID) ||
		(lineage.Encoding == WorkspaceLineageSlug && wf.Kind != KindText) {
		return fmt.Errorf("inherited read parent workspace column is invalid")
	}
	for _, idx := range parent.Indexes {
		if idx.Unique && len(idx.Columns) == 2 && idx.Columns[0] == ColTenantID && idx.Columns[1] == s.ParentColumn {
			return nil
		}
	}
	return fmt.Errorf("inherited read parent reference must be unique within its tenant")
}
