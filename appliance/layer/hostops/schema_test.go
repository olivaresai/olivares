// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops_test

import (
	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"os"
	"reflect"
	"testing"
)

func TestDescriptor_SchemaFixtureIsTheCommonRendererDefinition(t *testing.T) {
	data, err := os.ReadFile("schema/fixtures/descriptor.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture hostops.Descriptor
	if err := hostops.DecodeClosed(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture, hostops.StatusDescriptor()) {
		t.Fatal("schema fixture and common descriptor differ")
	}
}
func TestDescriptor_RefusesMissingListsAndControlText(t *testing.T) {
	for _, change := range []func(*hostops.Descriptor){
		func(d *hostops.Descriptor) { d.Preconditions = nil },
		func(d *hostops.Descriptor) { d.InputSchema.Required = nil },
		func(d *hostops.Descriptor) { d.Preconditions = []string{"terminal\x1b[31m"} },
	} {
		d := hostops.StatusDescriptor()
		change(&d)
		if _, err := hostops.NewCatalog([]hostops.Descriptor{d}); err == nil {
			t.Fatal("incomplete descriptor accepted")
		}
	}
}
