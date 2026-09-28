// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dnfrefresh

import (
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/license/aptrefresh"
)

// TestDnfCredentialIsNotAnAptCredential: a credential whose audience is the DNF root is not an
// apt-refresh answer. Carried as apt_credential, or in a DNF-shaped answer, it is ErrAnswer, and
// that outcome is unknown, so the apt-refresh operation stays pending.
func TestDnfCredentialIsNotAnAptCredential(t *testing.T) {
	b := aptrefresh.Binding{DeploymentID: testBinding.DeploymentID, PopKID: testBinding.PopKID, BindingEpoch: testBinding.BindingEpoch}
	inApt := answerFixture(t, nil, func(a map[string]any) {
		a["apt_credential"] = a["dnf_credential"]
		delete(a, "dnf_credential")
	})
	cred, _ := inApt["apt_credential"].(string)
	_, err := aptrefresh.CheckAnswer(inApt, b, testNow)
	if !errors.Is(err, aptrefresh.ErrAnswer) {
		t.Fatal("a DNF audience in apt_credential was accepted")
	}
	if credentialLeaked(err.Error(), cred) {
		t.Fatal("the apt-refresh refusal carries the credential")
	}
	if o, code := aptrefresh.OutcomeOf(fmt.Errorf("check: %w", err)); o != aptrefresh.OutcomeUnknown || code != "" {
		t.Fatalf("outcome %s/%q, want unknown so the operation stays pending", o, code)
	}
	dnfShaped := answerFixture(t, nil, nil)
	if _, err := aptrefresh.CheckAnswer(dnfShaped, b, testNow); !errors.Is(err, aptrefresh.ErrAnswer) {
		t.Fatal("a DNF-shaped answer was accepted as apt-refresh")
	}
}
