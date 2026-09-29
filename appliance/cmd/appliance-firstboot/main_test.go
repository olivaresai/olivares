// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/base"
)

func TestReconcile_ExitsByOutcomeAndNamesTheUnitToRunNext(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		says string
	}{
		{"reconciled", nil, 0, "sudo systemctl start olivares-appliance-firstboot.service"},
		{"refused: the product may have started", base.Refuse("the product may have started"), 1, "cannot reconcile: refused"},
		{"refused: another record schema", &base.SchemaError{Found: "olivares-appliance-firstboot/v9"}, 1, "v9"},
		{"pending: no carrier", base.Wait("no answers carrier is present"), 2, "cannot reconcile: pending"},
		{"the record cannot be written", errors.New("disk full"), 2, "disk full"},
	}
	for _, tc := range cases {
		code, line := reconcileOutcome(base.Record{}, tc.err)
		if code != tc.code || !strings.Contains(line, tc.says) {
			t.Fatalf("%s: exit %d %q, want %d and %q", tc.name, code, line, tc.code, tc.says)
		}
	}
	if code, line := reconcileOutcome(base.Record{}, nil); code != 0 || strings.Contains(line, "appliance-firstboot apply") {
		t.Fatalf("the next step runs outside the unit: %q", line)
	}
}

func TestHandOverHostSettingsCommand_RequiresVerifiedRecord(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "unverified", true: "verified"}[verified], func(t *testing.T) {
			root := t.TempDir()
			store := base.Store{Dir: filepath.Join(root, "state")}
			if err := os.MkdirAll(store.Dir, 0700); err != nil {
				t.Fatal(err)
			}
			rec := base.Record{Schema: "olivares-appliance-firstboot/v1", State: base.Pending}
			if verified {
				rec.Completed = []base.Completed{{Stage: base.StageHostSettings, Effect: "verified"}}
			}
			if err := store.Save(rec); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			code := handOverHostSettings(store, base.HostSettingsHandoff{Host: base.Host{Root: root}}, &output, &output)
			_, err := os.Stat(filepath.Join(root, base.HostOwnerFile))
			if verified {
				if code != 0 || err != nil {
					t.Fatalf("verified handoff failed: %d %v", code, err)
				}
			} else if code != 1 || !os.IsNotExist(err) {
				t.Fatalf("unverified handoff had effect: %d %v", code, err)
			}
		})
	}
	root := t.TempDir()
	var output bytes.Buffer
	if code := handOverHostSettings(base.Store{Dir: filepath.Join(root, "state")}, base.HostSettingsHandoff{Host: base.Host{Root: root}}, &output, &output); code != 1 {
		t.Fatalf("missing record exit %d", code)
	}
}

func TestHandOverHostSettingsCommand_UsesTheExistingLock(t *testing.T) {
	root := t.TempDir()
	store := base.Store{Dir: filepath.Join(root, "state")}
	unlock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	var out bytes.Buffer
	if code := handOverHostSettings(store, base.HostSettingsHandoff{Host: base.Host{Root: root}}, &out, &out); code != 2 {
		t.Fatalf("contending handoff exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, base.HostOwnerFile)); !os.IsNotExist(err) {
		t.Fatal("contending handoff touched owner file")
	}
}
