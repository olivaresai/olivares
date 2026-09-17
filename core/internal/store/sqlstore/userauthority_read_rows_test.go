// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type userAuthorityReadRow struct {
	id, tenant string
	version    int64
	storage    [3]string
}
type userAuthorityReadFaultRows struct {
	values                         []userAuthorityReadRow
	next                           int
	scanErr, iteratorErr, closeErr error
	closed                         bool
}

func (r *userAuthorityReadFaultRows) Next() bool {
	if r.next == len(r.values) {
		return false
	}
	r.next++
	return true
}
func (r *userAuthorityReadFaultRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	row := r.values[r.next-1]
	*dest[0].(*string), *dest[1].(*string), *dest[2].(*int64) = row.id, row.tenant, row.version
	if len(dest) == 6 {
		for i := range 3 {
			*dest[i+3].(*string) = row.storage[i]
		}
	}
	return nil
}
func (r *userAuthorityReadFaultRows) Err() error   { return r.iteratorErr }
func (r *userAuthorityReadFaultRows) Close() error { r.closed = true; return r.closeErr }

func TestUserAuthorityReadDecoder(t *testing.T) {
	id := model.NewID()
	good := userAuthorityReadRow{id: id.String(), tenant: model.SystemTenantID.String(), version: 7, storage: [3]string{"text", "text", "integer"}}
	scanErr := errors.New("injected Scan error")
	iterErr := errors.New("injected iterator error")
	closeErr := errors.New("injected Close error")
	for _, sqlite := range []bool{false, true} {
		name := "Postgres"
		if sqlite {
			name = "SQLite"
		}
		t.Run(name, func(t *testing.T) {
			cases := []struct {
				name   string
				rows   userAuthorityReadFaultRows
				causes []error
			}{
				{"valid", userAuthorityReadFaultRows{values: []userAuthorityReadRow{good}}, nil},
				{"absent", userAuthorityReadFaultRows{}, nil},
				{"duplicate", userAuthorityReadFaultRows{values: []userAuthorityReadRow{good, good}}, nil},
				{"scan", userAuthorityReadFaultRows{values: []userAuthorityReadRow{good}, scanErr: scanErr}, []error{scanErr}},
				{"iterator", userAuthorityReadFaultRows{values: []userAuthorityReadRow{good}, iteratorErr: iterErr}, []error{iterErr}},
				{"close", userAuthorityReadFaultRows{values: []userAuthorityReadRow{good}, closeErr: closeErr}, []error{closeErr}},
				{"cancellation-and-finalization", userAuthorityReadFaultRows{values: []userAuthorityReadRow{good}, scanErr: context.Canceled, iteratorErr: iterErr, closeErr: closeErr}, []error{context.Canceled, iterErr, closeErr}},
			}
			for _, field := range []string{"id", "tenant", "version", "id-storage", "tenant-storage", "version-storage"} {
				if !sqlite && len(field) > 7 {
					continue
				}
				row := good
				switch field {
				case "id":
					row.id = model.NewID().String()
				case "tenant":
					row.tenant = model.NewTenantID().String()
				case "version":
					row.version = 0
				case "id-storage":
					row.storage[0] = "blob"
				case "tenant-storage":
					row.storage[1] = "blob"
				case "version-storage":
					row.storage[2] = "real"
				}
				cases = append(cases, struct {
					name   string
					rows   userAuthorityReadFaultRows
					causes []error
				}{field, userAuthorityReadFaultRows{values: []userAuthorityReadRow{row}}, nil})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					got, err := decodeReadUserAuthority(&tc.rows, id, sqlite)
					if !tc.rows.closed {
						t.Fatal("rows were not finalized")
					}
					if tc.name == "valid" {
						if err != nil || got != (store.UserAuthorityFactRef{UserID: id, Version: 7}) {
							t.Fatalf("valid row: %v", err)
						}
						return
					}
					if got != (store.UserAuthorityFactRef{}) || !errors.Is(err, store.ErrDirectoryUnavailable) {
						t.Fatalf("bad row yielded evidence: %v", err)
					}
					for _, cause := range tc.causes {
						if !errors.Is(err, cause) {
							t.Fatalf("lost cause %v: %v", cause, err)
						}
					}
				})
			}
		})
	}
}
