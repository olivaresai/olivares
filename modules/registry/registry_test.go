// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package registry

import (
	"os/exec"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/modulespec"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/modules/sessioncockpit"
	"github.com/olivaresai/olivares/sdk"
)

func TestModuleSpecConstructorsAreCurrent(t *testing.T) {
	if output, err := exec.Command("python3", "../../scripts/generate-module-spec.py", "--check").CombinedOutput(); err != nil {
		t.Fatalf("module spec generation: %v\n%s", err, output)
	}
}

func TestModuleSpecBuildPreservesConfiguredInstances(t *testing.T) {
	evt := eventing.New()
	cockpit := sessioncockpit.NewPlaceholder()
	modules, err := Build([]api.Module{evt}, []api.Module{cockpit})
	if err != nil {
		t.Fatal(err)
	}
	specs := modulespec.All()
	if len(modules) != len(specs) {
		t.Fatalf("built %d modules for %d specs", len(modules), len(specs))
	}
	for i, m := range modules {
		if m.APINamespace() != specs[i].Namespace {
			t.Fatalf("registration order at %d: %s, want %s", i, m.APINamespace(), specs[i].Namespace)
		}
		if m.APINamespace() == "eventing" && m != evt {
			t.Fatal("configured eventing instance replaced")
		}
		if m.APINamespace() == "session-cockpit" && m != cockpit {
			t.Fatal("edition instance replaced")
		}
	}
}

func TestModuleSpecBuildRefusesAmbiguousComposition(t *testing.T) {
	evt := eventing.New()
	for _, configured := range [][]api.Module{{nil}, {evt, evt}} {
		if _, err := Build(configured, nil); err == nil {
			t.Fatalf("accepted invalid composition %v", configured)
		}
	}
}

type replacementModule struct {
	*sessioncockpit.Placeholder
	namespace string
}

func (m replacementModule) APINamespace() string       { return m.namespace }
func (m replacementModule) Descriptor() sdk.Descriptor { return sdk.Descriptor{Name: "replacement"} }

func TestModuleSpecBuildChecksConstructorInputsAndPreservesEditionNames(t *testing.T) {
	replacement := replacementModule{Placeholder: sessioncockpit.NewPlaceholder(), namespace: "eventing"}
	if _, err := Build([]api.Module{replacement}, nil); err == nil {
		t.Fatal("accepted an incompatible eventing constructor input")
	}
	replacement.namespace = "session-cockpit"
	modules, err := Build(nil, []api.Module{replacement})
	if err != nil {
		t.Fatal(err)
	}
	if modules[len(modules)-1] != replacement {
		t.Fatal("edition replacement lost")
	}
	replacement.namespace = "edition-extra"
	modules, err = Build(nil, []api.Module{replacement})
	if err != nil {
		t.Fatal(err)
	}
	if modules[len(modules)-1] != replacement {
		t.Fatal("additional edition module lost")
	}
	if _, err := Build([]api.Module{replacement}, nil); err == nil {
		t.Fatal("accepted unlisted configured module")
	}
	modules, err = Build(nil, []api.Module{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range modules {
		if m.APINamespace() == "session-cockpit" {
			t.Fatal("empty edition registration acquired a placeholder")
		}
	}
}
