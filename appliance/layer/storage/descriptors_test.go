// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

func TestStorageDescriptors_AreReadsWithWebCLIAndTUIParity(t *testing.T) {
	descriptors := storage.Descriptors()
	if len(descriptors) == 0 {
		t.Fatal("the storage module registers no task")
	}
	catalog, err := hostops.NewCatalog(append([]hostops.Descriptor{hostops.StatusDescriptor()}, descriptors...))
	if err != nil {
		t.Fatalf("the operation engine refuses the storage descriptors: %v", err)
	}
	for _, d := range descriptors {
		if d.Module != "storage" || d.Category != "Storage" || d.Confirmation != "none" || d.ActVerb != "" || d.Audience != "" {
			t.Fatalf("%s is not a read of the Storage category: %+v", d.ID, d)
		}
		for _, mode := range []string{"product-up", "product-down-repair", "unavailable"} {
			if !slices.Contains(d.Modes, mode) {
				t.Errorf("%s is not readable in mode %s", d.ID, mode)
			}
		}
		if !strings.HasPrefix(d.EquivalentCommand, "olivares-appliance storage "+d.Verb) {
			t.Errorf("%s names the command %q", d.ID, d.EquivalentCommand)
		}
		if !strings.Contains(d.Consequence, "no host setting changes") || !strings.Contains(d.Consequence, "not a data backup") {
			t.Errorf("%s consequence %q does not say that it changes nothing and that no metadata backup or snapshot is a data backup", d.ID, d.Consequence)
		}
		for _, surface := range []string{"web", "cli", "tui"} {
			got, err := catalog.Describe(d.Module, d.Verb, surface)
			if err != nil || !reflect.DeepEqual(got, d) {
				t.Fatalf("%s on %s: %+v %v", d.ID, surface, got, err)
			}
		}
		for _, input := range []string{`{"device":"/dev/sda"}`, `{"path":"/"}`} {
			if d.ValidateInput(json.RawMessage(input)) == nil {
				t.Errorf("%s accepts the input %s", d.ID, input)
			}
		}
	}
}
