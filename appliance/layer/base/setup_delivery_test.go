// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
)

// deliveryHost builds a host root with the product's account and directories in place: the
// data directory the storage stage verified and the unit's state directory, both under a
// temporary root, with the account at the test process's own ids so the ownership the seam
// gives the token file is observable unprivileged.
func deliveryHost(t *testing.T) (Host, string) {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, ProductDataDir)
	if err := os.MkdirAll(data, 0o750); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, StateDir)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	serviceAccount(t, root, os.Getuid())
	return Host{Root: root, Run: (&noProgram{}).run}, data
}

var deliveredTokenShape = regexp.MustCompile(`^olst_[A-Z2-7]+\n$`)

func TestSetupTokenDelivery_MintsThroughTheOwnerAndDeliversOnce(t *testing.T) {
	host, data := deliveryHost(t)
	seam := SetupTokenDelivery{Host: host}

	effect, err := seam.Apply(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	deliveryPath := filepath.Join(host.Root, StateDir, setupTokenDeliveryFile)
	delivered, err := os.ReadFile(deliveryPath)
	if err != nil {
		t.Fatalf("the delivery file was not written: %v", err)
	}
	if !deliveredTokenShape.Match(delivered) {
		t.Fatalf("the delivered bytes are not the owner's token shape: %q", delivered)
	}
	info, err := os.Stat(deliveryPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the delivery file is not root-only 0600: %v %v", info, err)
	}

	// THE ENGINE'S OWN ACCEPTANCE, checked by the owner itself: the engine reads
	// <data>/setup.token as the product account and verifies the delivered plaintext
	// against it. This is the property that makes the delivered token usable at
	// https://<address>:8443/setup, not merely present on the disk.
	plaintext := strings.TrimSuffix(string(delivered), "\n")
	if !secure.NewSetupToken(filepath.Join(data, "setup.token")).Verify(plaintext) {
		t.Fatal("the delivered token does not verify against the owner's record; the engine would refuse it")
	}
	tokenInfo, err := os.Stat(filepath.Join(data, "setup.token"))
	if err != nil || tokenInfo.Mode().Perm() != 0o600 {
		t.Fatalf("the owner's token record is not 0600: %v %v", tokenInfo, err)
	}
	uid, _, ok := host.accountIDs(productAccount)
	st, statOk := tokenInfo.Sys().(*syscall.Stat_t)
	if !ok || !statOk || int(st.Uid) != uid {
		t.Fatalf("the token record is not owned by the product account (uid %d): %v", uid, err)
	}

	// The effect names where, never what.
	if strings.Contains(string(effect), plaintext) || strings.Contains(string(effect), "olst_") {
		t.Fatalf("the effect carries the token plaintext: %s", effect)
	}
	if !strings.Contains(string(effect), filepath.Join(StateDir, setupTokenDeliveryFile)) {
		t.Fatalf("the effect does not name the delivery path: %s", effect)
	}
}

func TestSetupTokenDelivery_SecondApplyDeliversNothingNew(t *testing.T) {
	host, _ := deliveryHost(t)
	seam := SetupTokenDelivery{Host: host}

	first, err := seam.Apply(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	deliveryPath := filepath.Join(host.Root, StateDir, setupTokenDeliveryFile)
	before, err := os.ReadFile(deliveryPath)
	if err != nil {
		t.Fatal(err)
	}

	// A run interrupted after the mint and before the record was persisted lands here
	// again: the token exists, the delivery exists, and neither changes.
	second, err := seam.Apply(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(deliveryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || first != second {
		t.Fatalf("a repeated apply changed the delivery:\nfirst effect  %s\nsecond effect %s", first, second)
	}
	if err := seam.Verify(context.Background(), Input{}, first); err != nil {
		t.Fatalf("the unchanged delivery does not verify: %v", err)
	}
}

func TestSetupTokenDelivery_RefusesATokenMintedButNeverDelivered(t *testing.T) {
	host, data := deliveryHost(t)
	// The owner mints out of band — an engine start before first boot, or a lost delivery
	// file — and no plaintext exists to deliver.
	if _, _, err := secure.NewSetupToken(filepath.Join(data, "setup.token")).Ensure(); err != nil {
		t.Fatal(err)
	}

	_, err := SetupTokenDelivery{Host: host}.Apply(context.Background(), Input{})
	if !refused(err) {
		t.Fatalf("an undeliverable token did not refuse: %v", err)
	}
	if !strings.Contains(err.Error(), "apply") || !strings.Contains(err.Error(), "setup.token") {
		t.Fatalf("the refusal does not name the operator's remedy: %v", err)
	}
}

func TestSetupTokenDelivery_VerifyRefusesAChangedDelivery(t *testing.T) {
	host, _ := deliveryHost(t)
	seam := SetupTokenDelivery{Host: host}
	recorded, err := seam.Apply(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	deliveryPath := filepath.Join(host.Root, StateDir, setupTokenDeliveryFile)
	if err := os.WriteFile(deliveryPath, []byte("olst_TAMPERED\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seam.Verify(context.Background(), Input{}, recorded); !refused(err) {
		t.Fatalf("a changed delivery verified: %v", err)
	}
}

func TestSetupTokenDelivery_RefusesWithoutTheProductAccount(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ProductDataDir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, StateDir), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := SetupTokenDelivery{Host: Host{Root: root, Run: (&noProgram{}).run}}.Apply(context.Background(), Input{})
	if !refused(err) || !strings.Contains(err.Error(), "product service account") {
		t.Fatalf("a missing product account did not refuse as the storage stage does: %v", err)
	}
}

// refused reports whether err is a Refused outcome.
func refused(err error) bool {
	if err == nil {
		return false
	}
	var outcome *Outcome
	return errors.As(err, &outcome) && outcome.State == Refused
}
