// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

func TestCustodyAssertions(t *testing.T) {
	t.Setenv(envKeyCustody, "byok")
	t.Setenv(envLedgerCustody, "hyok")
	a, err := loadCustodyAssertions()
	if err != nil {
		t.Fatal(err)
	}
	// Declared BYOK satisfied by either source, refused for minted/CMEK.
	if err := a.verify(custodyModeBYOKFile, true); err != nil {
		t.Fatalf("byok+file should pass: %v", err)
	}
	if err := a.verify(custodyModeMinted, true); err == nil {
		t.Fatal("declared byok accepted a minted key")
	}
	if err := a.verify(custodyModeCMEK, true); err == nil {
		t.Fatal("declared byok accepted a cmek key (declare cmek instead)")
	}
	// Declared HYOK requires the off-box checkpoint signer.
	if err := a.verify(custodyModeBYOKFile, false); err == nil {
		t.Fatal("declared hyok accepted on-box checkpoints")
	}

	t.Setenv(envKeyCustody, "cmek")
	t.Setenv(envLedgerCustody, "")
	a, err = loadCustodyAssertions()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.verify(custodyModeCMEK, false); err != nil {
		t.Fatalf("cmek+cmek should pass: %v", err)
	}
	if err := a.verify(custodyModeBYOKEnv, false); err == nil {
		t.Fatal("declared cmek accepted a byok key")
	}

	t.Setenv(envKeyCustody, "fort-knox")
	if _, err := loadCustodyAssertions(); err == nil {
		t.Fatal("unknown custody assertion accepted")
	}
}
