// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/secure"
)

// Only Community composes recording off. A Business build that lost the option would stop
// recording privileged sessions without a word, so the production module set is built under
// each edition and its recorder is asked about a privileged call. With no data handle yet, a
// Business recorder denies closed and a Community recorder records nothing and denies nothing.
func TestBuildModulesComposesRecordingByEdition(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	call := api.RecordedCall{Namespace: "governance", Method: "GET", Pattern: "/things"}
	for _, tc := range []struct {
		edition  string
		wantDeny bool
	}{
		{"community", false},
		{"enterprise", true},
	} {
		t.Run(tc.edition, func(t *testing.T) {
			thisEdition.name = tc.edition
			dir := t.TempDir()
			priv, _, err := secure.LoadOrCreateSigningKey(filepath.Join(dir, "audit-signing.key"))
			if err != nil {
				t.Fatal(err)
			}
			signer, err := audit.NewSigner(priv)
			if err != nil {
				t.Fatal(err)
			}
			log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
			set, err := buildModules(nil, signer, nil, nil, nil, nil, nil, sourcesConfig{}, EditionConfig{DataDir: dir}, dir, log)
			if err != nil {
				t.Fatalf("build modules: %v", err)
			}
			dec, err := set.recorder.Gate(context.Background(), call)
			if (err != nil) != tc.wantDeny || dec.Record {
				t.Fatalf("%s recorder Gate = %+v, %v; want deny=%v and no recording without a data handle", tc.edition, dec, err, tc.wantDeny)
			}
		})
	}
}
