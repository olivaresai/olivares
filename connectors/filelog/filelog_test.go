// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package filelog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "secret write blocked",
		Body:     "claude-1 denied",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"agent": "claude-1"},
		Time:     time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

func openFile(t *testing.T, format string) (*Output, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.log")
	o := New()
	cfg := map[string]string{"path": path}
	if format != "" {
		cfg["format"] = format
	}
	if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o, path
}

func TestAppendJSONLines(t *testing.T) {
	o, path := openFile(t, "json")
	defer o.Close(context.Background())

	for i := 0; i < 3; i++ {
		if err := o.Notify(context.Background(), sampleNotification()); err != nil {
			t.Fatalf("Notify: %v", err)
		}
	}
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %q", len(lines), string(data))
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &v); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	if v["severity"] != "high" || v["title"] != "secret write blocked" {
		t.Errorf("json line wrong: %v", v)
	}
}

func TestTextFormatsAreOneLineEach(t *testing.T) {
	for _, f := range []string{"cef", "leef", "syslog"} {
		o, path := openFile(t, f)
		if err := o.Notify(context.Background(), sampleNotification()); err != nil {
			t.Fatalf("%s Notify: %v", f, err)
		}
		_ = o.Close(context.Background())
		data, _ := os.ReadFile(path)
		if n := strings.Count(strings.TrimRight(string(data), "\n"), "\n"); n != 0 {
			t.Errorf("%s record spans %d extra lines: %q", f, n, string(data))
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Errorf("%s produced no record", f)
		}
	}
}

func TestAppendModePreservesPriorContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	if err := os.WriteFile(path, []byte("PRE-EXISTING\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": path}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "PRE-EXISTING\n") {
		t.Errorf("append mode truncated the file: %q", string(data))
	}
}

func TestFsyncDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"path": path, "fsync": "true",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if !o.fsync {
		t.Fatal("fsync config not honored")
	}
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify with fsync: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "secret write blocked") {
		t.Errorf("fsync'd record not on disk: %q", string(data))
	}
}

func TestStandardStreamTargetsNotClosed(t *testing.T) {
	for _, target := range []string{"stdout", "stderr", "-"} {
		o := New()
		if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": target}}); err != nil {
			t.Fatalf("%s: Open: %v", target, err)
		}
		if o.closer {
			t.Errorf("%s: a standard stream must not be marked for closing", target)
		}
		// Close must be a no-op (it must not close the process's std stream).
		if err := o.Close(context.Background()); err != nil {
			t.Errorf("%s: Close = %v, want nil", target, err)
		}
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	for i, cfg := range []map[string]string{
		{}, // missing path
		{"path": "/tmp/x.log", "format": "weird"}, // bad format
		{"path": "/nonexistent-dir-xyz/x.log"},    // unwritable
	} {
		o := New()
		if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err == nil {
			t.Errorf("case %d: Open(%v) = nil, want error", i, cfg)
		}
	}
}

func TestNotifyBeforeOpen(t *testing.T) {
	o := New()
	if err := o.Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("Notify before Open must error")
	}
}
