// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestCommunityAuditRecoverArchiveUnavailableBeforeBoot(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "existing"}[existing], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "not-created")
			tenant := "22222222-2222-7222-8222-222222222222"
			pubSpec := strings.Repeat("A", 43) + "="
			if existing {
				f := newRecoveryCLIFixture(t, true, false)
				dir, tenant, pubSpec = f.dataDir, f.tenant.String(), f.pubSpec
			}
			archiveDir := filepath.Join(t.TempDir(), "archive")
			before := recoveryDirectorySnapshot(t, dir)
			archiveBefore := recoveryDirectorySnapshot(t, archiveDir)
			cmd := newAuditCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"recover", "--tenant", tenant, "--pubkey", pubSpec, "--data-dir", dir, "--archive-dir", archiveDir})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "Business") || exitcode.From(err) != exitcode.Edition {
				t.Errorf("archive recovery = %v (exit %d), want Business refusal, exit 9", err, exitcode.From(err))
			}
			if after := recoveryDirectorySnapshot(t, dir); !reflect.DeepEqual(before, after) {
				t.Errorf("archive recovery changed data directory %s", dir)
			}
			if after := recoveryDirectorySnapshot(t, archiveDir); !reflect.DeepEqual(archiveBefore, after) {
				t.Errorf("archive recovery changed archive directory %s", archiveDir)
			}
		})
	}
}

func recoveryDirectorySnapshot(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	files := make(map[string][32]byte)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if os.IsNotExist(err) && path == dir {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			files[path] = [32]byte{}
			return nil
		}
		data, err := os.ReadFile(path)
		if err == nil {
			files[path] = sha256.Sum256(data)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestCommunityAuditExportCommandsUnavailable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	for _, args := range [][]string{
		{"export", "--tenant", "22222222-2222-7222-8222-222222222222", "--data-dir", dir},
		{"archive", "export", "--tenant", "22222222-2222-7222-8222-222222222222", "--data-dir", dir, "--out", dir},
		{"archive", "verify", "--dir", dir, "--strict"},
	} {
		cmd := newAuditCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "Business") || exitcode.From(err) != exitcode.Edition {
			t.Errorf("%v = %v (exit %d), want Business refusal, exit 9", args, err, exitcode.From(err))
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("unavailable command touched data directory: %v", err)
	}
}

func TestCommunityDirectoryArchiveRefusesStartup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	loop, err := newAuditArchiveLoop(auditArchiveConfig{sink: "dir", dir: dir}, nil, nil, nil, discardLog())
	if loop != nil || err == nil || !strings.Contains(err.Error(), "Business") || !strings.Contains(err.Error(), auditArchiveSinkEnv) {
		t.Fatalf("loop = %v %v", loop, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("refusal created archive: %v", err)
	}
}

// Community verifies its own ledger: an intact chain exits 0, a tampered row fails
// --strict with exit 1 and the report names the first broken sequence. Neither run
// writes the data directory.
func TestCommunityAuditVerifyChecksLocalLedger(t *testing.T) {
	for _, tampered := range []bool{false, true} {
		t.Run(map[bool]string{false: "intact", true: "tampered"}[tampered], func(t *testing.T) {
			f := newRecoveryCLIFixture(t, false, tampered)
			// A read-only reader of a WAL database may create SQLite's sidecars, as
			// for every offline reader; the database and every other file stay identical.
			snapshot := func() map[string][32]byte {
				files := recoveryDirectorySnapshot(t, f.dataDir)
				for _, name := range []string{"olivares.db-wal", "olivares.db-shm"} {
					delete(files, filepath.Join(f.dataDir, name))
				}
				return files
			}
			before := snapshot()
			cmd := newAuditCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			args := []string{"verify", "--tenant", f.tenant.String(), "--data-dir", f.dataDir}
			if tampered {
				args = append(args, "--strict")
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			var report struct {
				Chain struct {
					OK      bool
					Checked int64
					BreakAt int64
				} `json:"chain"`
			}
			if jerr := json.Unmarshal(out.Bytes(), &report); jerr != nil {
				t.Fatalf("verify = %v (exit %d), printed no report: %v\n%s", err, exitcode.From(err), jerr, out.String())
			}
			if tampered {
				if exitcode.From(err) != exitcode.Err || report.Chain.OK || report.Chain.BreakAt != f.breakAt {
					t.Fatalf("tampered verify = %v (exit %d), chain %+v, want exit 1 breaking at seq %d", err, exitcode.From(err), report.Chain, f.breakAt)
				}
			} else if err != nil || !report.Chain.OK || report.Chain.Checked == 0 {
				t.Fatalf("intact verify = %v (exit %d), chain %+v, want exit 0 over the whole chain", err, exitcode.From(err), report.Chain)
			}
			if after := snapshot(); !reflect.DeepEqual(before, after) {
				for path, sum := range after {
					if before[path] != sum {
						t.Errorf("Community verification wrote %s", path)
					}
				}
				for path := range before {
					if _, ok := after[path]; !ok {
						t.Errorf("Community verification removed %s", path)
					}
				}
			}
		})
	}
}

func TestCommunityAuditTreeEvidenceUnavailableBeforeBoot(t *testing.T) {
	for _, verb := range []string{"verify", "prove"} {
		t.Run(verb, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "not-created")
			before := recoveryDirectorySnapshot(t, dir)
			cmd := newAuditCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"tree", verb, "--data-dir", dir, "--tenant", "22222222-2222-7222-8222-222222222222"})
			err := cmd.Execute()
			if err == nil || exitcode.From(err) != exitcode.Edition {
				t.Fatalf("tree %s = %v (exit %d), want Business refusal, exit 9", verb, err, exitcode.From(err))
			}
			if after := recoveryDirectorySnapshot(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatal("Community tree evidence changed stored data")
			}
		})
	}
}

func TestCommunityAuditTreeCheckpointRemainsAvailable(t *testing.T) {
	f := newRecoveryCLIFixture(t, false, false)
	cmd := newAuditCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"tree", "checkpoint", "--data-dir", f.dataDir, "--tenant", f.tenant.String()})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "olivares.ai/audit/"+f.tenant.String()+"\n") {
		t.Fatalf("checkpoint is not a signed note: %q", out.String())
	}
}
