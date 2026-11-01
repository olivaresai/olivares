// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSELinuxFileTypeTreatsMissingLabelsAsEmptyAndRefusesEACCES(t *testing.T) {
	restore := lgetxattr
	t.Cleanup(func() { lgetxattr = restore })
	for _, tc := range []struct {
		name    string
		err     error
		want    string
		wantErr bool
	}{
		{name: "ENODATA", err: unix.ENODATA, want: ""},
		{name: "ENOTSUP", err: unix.ENOTSUP, want: ""},
		{name: "EACCES", err: unix.EACCES, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lgetxattr = func(string, string, []byte) (int, error) { return -1, tc.err }
			got, err := selinuxFileType("/fixture")
			if tc.wantErr {
				if !errors.Is(err, tc.err) {
					t.Fatalf("error = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("type, error = %q, %v; want %q, nil", got, err, tc.want)
			}
		})
	}
}
