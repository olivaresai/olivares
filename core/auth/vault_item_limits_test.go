// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Each document below breaks exactly one rule; the refusal names a field and repeats no
// value.
func TestParseVaultItemRefusesEachBrokenRule(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	for name, raw := range map[string]string{
		"totp item with a password":                    `{"type":"totp","totp":"` + vaultSeed + `","password":"pw-0451"}`,
		"totp item with a user":                        `{"type":"totp","totp":"` + vaultSeed + `","username":"ann-0451"}`,
		"totp item with a website":                     `{"type":"totp","totp":"` + vaultSeed + `","url":"https://x.example/0451"}`,
		"note with a website":                          `{"type":"note","note":"x","url":"https://x.example/0451"}`,
		"note with a user":                             `{"type":"note","note":"x","username":"ann-0451"}`,
		"note with a key":                              `{"type":"note","note":"x","totp":"` + vaultSeed + `"}`,
		"password too long":                            `{"type":"login","password":"` + long(1025) + `"}`,
		"user too long":                                `{"type":"login","username":"` + long(1025) + `"}`,
		"website too long":                             `{"type":"login","username":"ann","url":"https://x.example/` + long(2048) + `"}`,
		"login with a bad key":                         `{"type":"login","password":"pw-0451","totp":"not a key 0451!"}`,
		"website with a password":                      `{"type":"login","username":"ann","url":"https://ann:pw-0451@bank.example"}`,
		"website that does not parse, with a password": `{"type":"login","username":"ann","url":"https://ann:pw-0451@bank.example:x"}`,
		"website with a password and no slashes":       `{"type":"login","username":"ann","url":"ann:pw-0451@bank.example"}`,
		"link that does not parse":                     `{"type":"totp","totp":"otpauth://totp/Bank%zz?secret=` + vaultSeed + `"}`,
	} {
		item, err := auth.ParseVaultItem(raw)
		if err == nil {
			t.Errorf("%s: accepted as %+v", name, item)
			continue
		}
		for _, secret := range []string{"0451", vaultSeed} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the refusal repeats a secret: %v", name, err)
			}
		}
	}
}

// The period and the digit count of a link decide the code and the seconds left: a
// hard-coded 30 or 6 would pass the RFC vectors and fail here.
func TestVaultItemTOTPCodeHonorsPeriodAndDigits(t *testing.T) {
	item, err := auth.ParseVaultItem(`{"type":"totp","totp":"otpauth://totp/Bank:ann?secret=` + vaultSeed + `&period=60&digits=7"}`)
	if err != nil {
		t.Fatal(err)
	}
	at := func(unix int64) auth.VaultCode {
		t.Helper()
		code, err := item.TOTPCode(time.Unix(unix, 0))
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	first, last, next := at(60), at(119), at(120)
	if first.Code != last.Code || first.Digits != 7 || len(first.Code) != 7 || first.Period != 60 {
		t.Fatalf("one 60 s window gave %+v and %+v", first, last)
	}
	if first.SecondsLeft != 60 || last.SecondsLeft != 1 || next.SecondsLeft != 60 || next.Code == last.Code {
		t.Fatalf("window edges: %+v %+v %+v", first, last, next)
	}
}

// Every member may write to the organization's scope, so a vault is bounded and an
// existing item can always be replaced.
func TestPutVaultBoundsAnOwnersItemsAndDescriptions(t *testing.T) {
	svc := vaultStore(t)
	ctx := context.Background()
	tenant := model.TenantID("tenant-vault-limits")
	owner, other := model.NewID().String(), model.NewID().String()
	put := func(who string, i int) error {
		_, err := svc.PutVault(ctx, adminActor(), tenant, fmt.Sprintf("vault/%s/item-%d", who, i),
			auth.VaultItem{Type: "note", Note: "x"}, "")
		return err
	}
	for i := 0; i < 500; i++ {
		if err := put(owner, i); err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
	}
	if err := put(owner, 500); err == nil {
		t.Fatal("a 501st item was stored")
	}
	if err := put(owner, 7); err != nil {
		t.Fatalf("replacing an item of a full vault: %v", err)
	}
	if err := put(other, 0); err != nil {
		t.Fatalf("another owner's first item: %v", err)
	}
	if _, err := svc.PutVault(ctx, adminActor(), tenant, "vault/"+other+"/long",
		auth.VaultItem{Type: "note", Note: "x"}, strings.Repeat("d", 1025)); err == nil {
		t.Fatal("a 1025-byte description was stored")
	}
}

// A stored document is read back by the same parser that admitted it: the encoder must not
// grow it past the bound (json.Marshal writes each < > & as six bytes), and PutVault holds the
// bound for a caller that did not come through ParseVaultItem.
func TestPutVaultStoresADocumentTheVaultCanReadBack(t *testing.T) {
	svc := vaultStore(t)
	ctx := context.Background()
	tenant := model.TenantID("tenant-vault-size")
	owner := model.NewID().String()

	name := "vault/" + owner + "/escapes"
	item := auth.VaultItem{Type: auth.VaultTOTP, TOTP: vaultSeed, Note: strings.Repeat("<", 20_000)}
	if _, err := svc.PutVault(ctx, adminActor(), tenant, name, item, ""); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := svc.VaultTOTP(ctx, adminActor(), tenant, name, time.Unix(59, 0)); err != nil {
		t.Fatalf("an item PutVault stored cannot make its code: %v", err)
	}

	huge := auth.VaultItem{Type: auth.VaultNote, Note: strings.Repeat("a", 70_000)}
	if _, err := svc.PutVault(ctx, adminActor(), tenant, "vault/"+owner+"/huge", huge, ""); !errors.Is(err, auth.ErrVaultItem) {
		t.Fatalf("a 70000-byte note: %v, want ErrVaultItem", err)
	}
}
