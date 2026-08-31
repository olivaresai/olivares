// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/model"
)

func TestSourcesListPrintsSourceMode(t *testing.T) {
	dir := t.TempDir()
	seed := &cobra.Command{}
	seed.SetContext(context.Background())
	eng, err := auditBoot(seed, dir, "sqlite", "")
	if err != nil {
		t.Fatalf("seed boot: %v", err)
	}
	putRow(t, eng.sourceStore, model.SourceDef{
		Name: "wiki-live", Kind: "confluence", Tenant: "acme", Enabled: true,
		Config: map[string]string{"mode": "live"},
	})
	putRow(t, eng.sourceStore, model.SourceDef{
		Name: "drive-default", Kind: "gdrive", Tenant: "acme", Enabled: true,
	})
	if err := eng.Close(); err != nil {
		t.Fatalf("close seed engine: %v", err)
	}

	var out bytes.Buffer
	cmd := newSourcesCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"ls", "--data-dir", dir, "--engine", "sqlite"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sources ls: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"MODE",
		"wiki-live",
		"live",
		"drive-default",
		"export",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sources ls output missing %q:\n%s", want, got)
		}
	}
}
