// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"slices"
	"testing"
	"time"
)

func TestModuleProfileEventingDefaultPreservesExplicitSelection(t *testing.T) {
	t.Setenv(envKeyWrap, "")
	t.Setenv(envCommunicationActivation, "")
	t.Setenv(envCommunicationContentKeyringFile, "")
	t.Setenv(envCommunicationCursorKeyringFile, "")
	for _, tc := range []struct {
		name     string
		selected []string
		explicit bool
	}{
		{name: "fresh"}, {name: "empty", selected: []string{}, explicit: true}, {name: "saved", selected: []string{"identity"}, explicit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: version, Logger: discardLogger(), ApplyModuleProfile: true}
			if tc.explicit {
				if err := saveNodeModuleSelection(cfg.DataDir, tc.selected, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				eng := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
				if eng.moduleProfile.Active("eventing") == tc.explicit {
					t.Errorf("boot %d eventing active=%t", attempt, eng.moduleProfile.Active("eventing"))
				}
				if tc.explicit && !slices.Equal(recordedSelection(t, newProductSettings(eng.store, eng.dataDir)), tc.selected) {
					t.Error("persisted explicit selection changed")
				}
				if readiness := mustCompositionReadiness(t, eng); readiness.Effective == tc.explicit {
					t.Errorf("boot %d readiness=%+v", attempt, readiness)
				}
				_ = eng.Close()
			}
		})
	}
	published := published26100ModuleSelection()
	if len(published) != 34 || !slices.Contains(published, "eventing") || slices.Contains(published, "skills") {
		t.Fatal("published module selection changed")
	}
}
