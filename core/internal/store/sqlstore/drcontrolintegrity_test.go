// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/store"
)

func TestDRClosedLocalRecordReachesOpenAndBootAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func([]byte) []byte
		reason string
	}{
		{"second_document", func(b []byte) []byte { return append(b, []byte(` {"state":"pending"}`)...) }, "one JSON document"},
		{"garbage", func(b []byte) []byte { return append(b, 'x') }, "one JSON document"},
		{"duplicate", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"format":1`), []byte(`"format":1,"format":1`), 1)
		}, "duplicate JSON member"},
		{"nested_duplicate", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"engine":"sqlite"`), []byte(`"engine":"sqlite","engine":"sqlite"`), 1)
		}, "duplicate JSON member"},
		{"format", func(b []byte) []byte { return bytes.Replace(b, []byte(`"format":1`), []byte(`"format":2`), 1) }, "record format"},
		{"digest", func(b []byte) []byte {
			return bytes.Replace(b, []byte(drFactualKeyset().SHA256), []byte(strings.Repeat("a", 64)), 1)
		}, "keyset digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "olivares.db")
			a, _, err := opgate.AnchorForStoreFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rec := localControl(a, opgate.StateComplete)
			b, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			writeLocalControl(t, a, rec)
			if err := os.WriteFile(a.RecordPath(), tc.edit(b), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: path}, nil)
			if st != nil {
				_ = st.Close()
			}
			if !errors.Is(err, ErrRestorePublicationFenced) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("actual Open did not consume parser refusal: %v", err)
			}
			adm, err := BeginLocalAdmission(ctx, store.Config{Engine: store.EngineSQLite, DSN: path}, dir)
			if adm != nil {
				adm.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("boot admission: %v", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("refusal created database: %v", err)
			}
		})
	}
	t.Run("whitespace_positive", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "positive.db")
		a, _, err := opgate.AnchorForStoreFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeLocalControl(t, a, localControl(a, opgate.StateComplete))
		f, err := os.OpenFile(a.RecordPath(), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(" \n\r\t")
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		// ⚠ THE SUBJECT HERE IS THE CODEC, NOT CUSTODY, and the assertion changed shape
		// with IR5-C2.
		//
		// This leg exists to prove the record parser accepts trailing whitespace after
		// the JSON document. It used to say so by opening the destination directly and
		// requiring success — which stopped being available when a direct Open of a
		// COMPLETED local control began demanding the custody that control authorized
		// (independent review F2). Success is therefore asserted where it is still
		// expressible: the direct Open must fail for CUSTODY rather than for a parse
		// reason, and the same whitespace-suffixed record must then publish through the
		// admission that supplies the authorized observation. Together those prove the
		// record was read, validated and understood.
		ctx := context.Background()
		cfg := store.Config{Engine: store.EngineSQLite, DSN: path}
		st, err := Open(ctx, cfg, nil)
		if st != nil {
			_ = st.Close()
		}
		if err == nil {
			t.Fatal("a direct Open published a completed local control with no custody observation")
		}
		if !strings.Contains(err.Error(), "no observation of the custody it actually loaded") {
			t.Fatalf("the whitespace-suffixed record did not parse; refusal was not the custody one: %v", err)
		}
		adm, aerr := BeginLocalAdmission(ctx, cfg, filepath.Dir(path))
		if aerr != nil {
			t.Fatalf("the admission refused a whitespace-suffixed completed record: %v", aerr)
		}
		defer adm.Close()
		served, oerr := adm.Open(ctx, nil, drObservationFor(t, drFactualKeyset()), nil)
		if oerr != nil {
			t.Fatalf("the whitespace-suffixed record did not publish under its authorized custody: %v", oerr)
		}
		_ = served.Close()
	})
}
func TestDRRecordFIFORefusesThroughActualOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo.db")
	a, _, err := opgate.AnchorForStoreFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(a.RecordPath(), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: path}, nil)
		if st != nil {
			_ = st.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRestorePublicationFenced) || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Open blocked on record FIFO")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("FIFO refusal created database: %v", err)
	}
}
