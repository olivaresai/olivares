// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/store"
)

func TestConsoleCustodyReportUsesBootSealerOverrides(t *testing.T) {
	dir := t.TempDir()
	key := []byte(strings.Repeat("s", 32))
	manifest := &dr.Manifest{}
	for _, k := range dr.SealerKeyFiles {
		t.Setenv(k.Env, "")
		if err := os.WriteFile(filepath.Join(dir, k.Name), []byte(base64.StdEncoding.EncodeToString(key)), 0600); err != nil {
			t.Fatal(err)
		}
		manifest.SealerProbes = append(manifest.SealerProbes, dr.SealerProbe{Name: k.Name, Probe: dr.SealerKeyProbe(key, k.Name)})
	}
	setActivationOverlayForTest(map[string]string{dr.SecretStoreKeyEnv: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("d", 32)))})
	t.Cleanup(func() { setActivationOverlayForTest(nil) })
	config := consoleDRConfig(bootConfig{DataDir: dir}, store.EngineSQLite, nil, nil)
	if config.Getenv == nil {
		t.Fatal("console restore must use boot's effective environment reader")
	}
	note := dr.RestoreCustodyNote("restore verified", manifest, dir, false, config.Getenv)
	if strings.Contains(note, "key custody intact") || !strings.Contains(note, "secret-store.key (OLIVARES_SECRET_STORE_KEY): OLIVARES_SECRET_STORE_KEY is not the source's key") {
		t.Fatalf("custody report ignored the effective boot override: %s", note)
	}
}
