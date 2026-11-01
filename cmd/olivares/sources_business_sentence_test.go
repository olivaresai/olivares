// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// The one Business source refusal names what the running build is actually missing:
// Community names the Business edition, and a commercial build that cannot construct the
// kind (base Business without identity-scale, which builds conjur) names the add-on
// instead of telling an operator who already runs Business to run it. This file carries
// no build tag so every build pins both sentences through the paths that share
// errBusinessSourceKind: the config validation, the unsupported-kind reason and the
// refusal itself.
func TestBusinessSourceRefusalNamesWhatTheBuildLacks(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })

	cases := []struct {
		edition editionPorts
		refusal string
	}{
		{
			edition: editionPorts{name: "community"},
			refusal: `connector unavailable in this edition: the "conjur" connector is a Business capability; run the Business edition or remove it from the configuration`,
		},
		{
			// The base Business build, and any commercial build without the add-on.
			edition: editionPorts{name: "enterprise"},
			refusal: `connector unavailable in this edition: the "conjur" connector requires Business Identity & Scale, which this build does not include; remove it from the configuration`,
		},
	}
	for _, c := range cases {
		thisEdition = c.edition
		got := errBusinessSourceKind("conjur")
		if !errors.Is(got, api.ErrConnectorUnavailable) {
			t.Errorf("edition %q: refusal %v is not ErrConnectorUnavailable", c.edition.name, got)
		}
		if got.Error() != c.refusal {
			t.Errorf("edition %q: refusal = %q, want %q", c.edition.name, got.Error(), c.refusal)
		}
		// The shared paths carry the same sentence: there is no second copy of it.
		if reason := unsupportedSourceKindReason("conjur"); reason != c.refusal {
			t.Errorf("edition %q: unsupportedSourceKindReason = %q, want the one refusal %q", c.edition.name, reason, c.refusal)
		}
		err := validateBusinessSourcesConfig(sourcesConfig{Sources: []sourceSpec{{Name: "a", Kind: "conjur", Tenant: "acme"}}})
		if !errors.Is(err, api.ErrConnectorUnavailable) || err.Error() != c.refusal {
			t.Errorf("edition %q: config refusal = %v, want %q", c.edition.name, err, c.refusal)
		}
	}
}
