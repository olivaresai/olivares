// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestToolCatalogRefusesUnsignedDriverWithoutConfinement(t *testing.T) {
	for _, tc := range []struct {
		name        string
		driver      ProviderDriver
		output      string
		want        []string
		wantRefused bool
	}{
		{
			name:        "grok unsigned bytes never run",
			driver:      NewGrokDriver(),
			output:      "You are logged in with grok.com.\n\nAvailable models:\n  * grok-4.6 (default)\n",
			wantRefused: true,
		},
		{
			name:   "opencode signed bytes still run",
			driver: NewOpenCodeDriver(),
			output: "opencode/glm-5.3-flash-free\n",
			want:   []string{"opencode/glm-5.3-flash-free"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			program := filepath.Join(root, "tool")
			script := "#!/bin/sh\nprintf ran > catalog-ran\nprintf '%s' '" + tc.output + "'\n"
			if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			// No WithConfinement: the native runner has no policy it can enforce.
			// ConfinementRequired must therefore refuse unsigned bytes before exec.
			m := New(WithRunner(NewProcRunner()), WithProviderDriver(tc.driver), WithDriverProgram(tc.driver.Key(), program))
			m.UseToolLoginsRoot(t.TempDir())
			tenant := model.TenantID(model.NewID())
			userHome, configHome, ok := m.OwnToolLoginHomes(tenant, tc.driver.Key())
			if !ok {
				t.Fatal("no own login home")
			}
			for _, dir := range []string{userHome, configHome} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			models, err := m.ReadOwnToolLoginModels(context.Background(), tenant, tc.driver.Key())
			marker := filepath.Join(userHome, "catalog-ran")
			if tc.wantRefused {
				if err == nil || len(models) != 0 {
					t.Fatalf("unsigned catalog ran without confinement: models=%v err=%v", models, err)
				}
				if !strings.Contains(err.Error(), "confinement") {
					t.Fatalf("catalog hid the confinement refusal: %v", err)
				}
				if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
					t.Fatalf("unsigned catalog executable ran: marker stat=%v", statErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(models, tc.want) {
				t.Fatalf("signed catalog changed: models=%v err=%v", models, err)
			}
			if _, statErr := os.Stat(marker); statErr != nil {
				t.Fatalf("signed catalog executable did not run: %v", statErr)
			}
		})
	}
}
