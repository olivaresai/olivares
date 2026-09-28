// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops

import (
	"encoding/json"
	"errors"
)

// Permissions is a trusted authority adapter's decision, never request input.
type Permissions struct {
	Read  bool   `json:"read"`
	Plan  bool   `json:"plan"`
	Apply bool   `json:"apply"`
	Code  string `json:"code,omitempty"`
}

// Access resolves the current caller's rights using the owning authority path.
// A surface must not derive this decision from a caller-supplied actor or mode.
// Operation first calls Permissions with only OperationID to admit the lookup,
// then with the full record to apply record-specific restrictions. Unknown
// authority at either stage must refuse.
type Access interface{ Permissions(Record) Permissions }

// ReadOnlyAccess is used after local OS admission. It does not grant an act.
type ReadOnlyAccess struct{}

func (ReadOnlyAccess) Permissions(Record) Permissions {
	return Permissions{Read: true, Plan: true, Code: "act_not_adopted"}
}

// View combines one current record and its current permissions for all surfaces.
type View struct {
	Record      Record      `json:"record"`
	Permissions Permissions `json:"permissions"`
}

// Reader is the shared operation read model. Neither views nor renderers issue effects.
type Reader interface {
	Get(string) (Record, int, error)
}

// Service keeps record and permission lookup together, without surface caches.
type Service struct {
	reader Reader
	access Access
}

func NewService(reader Reader, access Access) *Service {
	return &Service{reader: reader, access: access}
}

// Operation reads the same model for web, CLI and TUI. Surface selects rendering,
// never authority. Missing authority is refused before the model is read.
func (s *Service) Operation(id, surface string) (View, int, error) {
	if s == nil || s.reader == nil || s.access == nil {
		return View{}, 503, errors.New("p1_stage_missing")
	}
	if !oneOf(surface, "web", "cli", "tui") {
		return View{}, 422, refuse("surface")
	}
	if !s.access.Permissions(Record{OperationID: id}).Read {
		return View{}, 403, errors.New("mode_refused")
	}
	rec, status, err := s.reader.Get(id)
	if err != nil {
		return View{}, status, err
	}
	permissions := s.access.Permissions(rec)
	if !permissions.Read {
		return View{}, 403, errors.New("mode_refused")
	}
	return View{Record: rec, Permissions: permissions}, status, nil
}

// Task groups operation identifiers from one confirmed task. Its operations keep
// their own receipts and outcomes; the task does not create another effect ledger.
type Task struct {
	TaskID       string   `json:"task_id"`
	OperationIDs []string `json:"operation_ids"`
}

// Plan renders the descriptor and validated inputs before confirmation. It does
// not mint an operation id: the client does that only when confirming an act.
type Plan struct {
	Descriptor  Descriptor      `json:"descriptor"`
	Inputs      json.RawMessage `json:"inputs"`
	Permissions Permissions     `json:"permissions"`
}
