// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/sdk"
)

// closeCounter is an opened document source that counts its Close.
type closeCounter struct {
	contentsource.Source
	closed *int
}

func (c closeCounter) Close(context.Context) error { *c.closed++; return nil }

// A document source added from the console is wired on the running knowledge
// module without a restart; an imported one without its credential says so; and a
// new credential replaces the open source on the next apply.
func TestDocumentSourcesAreLiveRosterRows(t *testing.T) {
	ctx := context.Background()
	sr, _, _ := newOnboardHarness(t)
	sr.useKnowledge(knowledge.New(), nil)
	status := func(name string) string {
		rows, err := sr.ListSources(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.Name == name {
				return r.Status
			}
		}
		return "absent"
	}

	t.Run("fscontent is previewed and wired live", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "policy.md"), []byte("# Policy\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		in := api.ConnectorOnboardInput{Name: "handbook", Kind: "fscontent", Tenant: "acme", Enabled: true,
			Config: map[string]string{"root": root, "max_delete_percent": "20"}}
		preview, err := sr.PreviewConnector(ctx, recAdmin(), in)
		if err != nil || len(preview) != 1 || preview[0].Title == "" {
			t.Fatalf("preview = %+v, %v; want the one file", preview, err)
		}
		res, err := sr.PutConnector(ctx, recAdmin(), in)
		if err != nil || !res.Applied || res.Action != "added" {
			t.Fatalf("put = %+v, %v; want added live", res, err)
		}
		if got := status("handbook"); got != "running" {
			t.Fatalf("status = %q, want running", got)
		}
	})

	t.Run("imported without its credential", func(t *testing.T) {
		res, err := sr.PutConnector(ctx, recAdmin(), api.ConnectorOnboardInput{Name: "wiki", Kind: "confluence", Tenant: "acme", Enabled: true,
			Config: map[string]string{"mode": "live", "base_url": "https://wiki.example.invalid", "space_key": "ENG"}})
		if err != nil || res.Applied || !strings.Contains(res.Note, "credential missing") {
			t.Fatalf("put = %+v, %v; want persisted, not applied, credential missing", res, err)
		}
		if got := status("wiki"); got != "credential_missing" {
			t.Fatalf("status = %q, want credential_missing", got)
		}
	})

	t.Run("rotation reopens with the new credential", func(t *testing.T) {
		var seen []string
		closed := 0
		sr.openDocument = func(ctx context.Context, def model.SourceDef) (contentsource.Source, string) {
			src, _ := buildContentSource(def.Kind, nil)
			cfg, err := resolveConfig(ctx, sr.resolver, src.Descriptor(), sdk.Config{Settings: def.Config})
			if err != nil {
				return nil, "unresolved"
			}
			seen = append(seen, cfg.Settings["credential_ref"])
			return closeCounter{src, &closed}, ""
		}
		t.Cleanup(func() { sr.openDocument = sr.openDocumentSource })
		for _, token := range []string{"token-old", "token-new"} {
			res, err := sr.PutConnector(ctx, recAdmin(), api.ConnectorOnboardInput{Name: "wiki", Kind: "confluence", Tenant: "acme", Enabled: true,
				Config:  map[string]string{"mode": "live", "base_url": "https://wiki.example.invalid", "space_key": "ENG"},
				Secrets: map[string]string{"credential_ref": token}})
			if err != nil || !res.Applied {
				t.Fatalf("put %s = %+v, %v", token, res, err)
			}
		}
		if len(seen) != 2 || seen[0] != "token-old" || seen[1] != "token-new" || closed != 1 {
			t.Fatalf("opened with %q, closed %d; want the old then the new credential and the old source closed", seen, closed)
		}
	})
}
