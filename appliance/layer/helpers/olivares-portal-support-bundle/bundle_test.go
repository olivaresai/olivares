// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

func TestSupportBundle_ProducesIntoItsOwnSpoolAndStaysUnavailable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for path, content := range map[string]string{
		"etc/os-release":                  "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\nVERSION_ID=\"13\"\nHOME_URL=x\n",
		"proc/sys/kernel/osrelease":       "6.12.48+deb13-amd64\n",
		"proc/sys/kernel/random/boot_id":  "8d8a1f0c-54c0-4b3e-9d6a-2a1f3b4c5d6e\n",
		"etc/olivares-portal/tls.key":     "not collected\n",
		"var/lib/olivares/olivares.db.do": "not collected\n",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }
	h := helper(spool{dir: dir, random: rand.Reader, facts: hostFacts(root, now)})
	console := helperschema.Peer{UID: 0, Unit: helperschema.RepairConsole.Unit, Attested: true}

	produced := h.Perform(context.Background(), console, &helperschema.SupportBundleRequest{Op: helperschema.SupportBundleProduce})
	if produced.Result != helperschema.ResultPerformed || produced.Nonce == "" {
		t.Fatalf("produce answered %+v", produced)
	}
	if _, err := os.Lstat(filepath.Join(dir, "out", produced.Nonce+".bundle.json")); err != nil {
		t.Fatalf("the bundle is not in the spool's out directory under its nonce: %v", err)
	}

	fetched := h.Perform(context.Background(), console, &helperschema.SupportBundleRequest{Op: helperschema.SupportBundleFetch, Nonce: produced.Nonce})
	if fetched.Result != helperschema.ResultAnswered {
		t.Fatalf("fetch answered %+v", fetched)
	}
	var bundle map[string]any
	if err := json.Unmarshal(fetched.Bundle, &bundle); err != nil {
		t.Fatal(err)
	}
	osFields, _ := bundle["os"].(map[string]any)
	if bundle["schema_version"] != "olivares-support-bundle/v1" || bundle["collected_at"] != "2026-09-26T12:00:00Z" ||
		bundle["kernel_release"] != "6.12.48+deb13-amd64" || osFields["id"] != "debian" || osFields["version_id"] != "13" {
		t.Fatalf("the bundle is %s", fetched.Bundle)
	}
	if bytes.Contains(fetched.Bundle, []byte("not collected")) || bytes.Contains(fetched.Bundle, []byte("HOME_URL")) {
		t.Fatalf("the bundle carries more than its closed facts: %s", fetched.Bundle)
	}

	var other helperschema.SpoolKey
	foreign, err := helperschema.IssueNonce(other, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if answer := h.Perform(context.Background(), console, &helperschema.SupportBundleRequest{Op: helperschema.SupportBundleFetch, Nonce: string(foreign)}); answer.Result != helperschema.ResultRefused {
		t.Fatalf("a nonce this helper did not issue answered %+v", answer)
	}

	// No invoker is admitted for either subcommand yet, whoever asks.
	for _, subcommand := range []string{helperschema.SupportBundleProduce, helperschema.SupportBundleFetch} {
		var refusal *helperschema.Refusal
		err := helperschema.Admit(console, helperschema.SupportBundleRules(), subcommand)
		if !errors.As(err, &refusal) || refusal.Code != helperschema.CodeVerbUnavailable {
			t.Fatalf("%s for the repair console: %v, want %s", subcommand, err, helperschema.CodeVerbUnavailable)
		}
	}
}
