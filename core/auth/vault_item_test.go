// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// vaultSeed is the RFC 6238 SHA-1 test seed "12345678901234567890" in base32.
const vaultSeed = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestParseVaultItemAcceptsTheThreeKinds(t *testing.T) {
	for name, c := range map[string]struct{ raw, hint string }{
		"login":           {`{"type":"login","username":"ann","password":"pw-1","url":"https://bank.example"}`, "login"},
		"login with totp": {`{"type":"login","password":"pw-1","totp":"` + vaultSeed + `"}`, "login+totp"},
		"login by name":   {`{"type":"login","username":"ann"}`, "login"},
		// The exact document web/src/features/vault/model.test.ts says the console sends.
		"console document": {`{"type":"login","username":"ann","password":"pw","totp":"JBSWY3DPEHPK3PXP"}`, "login+totp"},
		"note":             {`{"type":"note","note":"the safe code is 4411"}`, "note"},
		"totp":             {`{"type":"totp","totp":"gezd gnbv gy3t qojq gezd gnbv gy3t qojq"}`, "totp"},
		"otpauth uri": {`{"type":"totp","totp":"otpauth://totp/Bank:ann?secret=` + vaultSeed +
			`&issuer=Bank&algorithm=SHA256&digits=8&period=60"}`, "totp"},
	} {
		item, err := auth.ParseVaultItem(c.raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := item.Hint(); got != c.hint {
			t.Errorf("%s: hint = %q, want %q", name, got, c.hint)
		}
		// What is stored is the normalized document: parsing it again changes nothing.
		again, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		back, err := auth.ParseVaultItem(string(again))
		if err != nil || back != item {
			t.Errorf("%s: normalized document does not round trip: %+v vs %+v (%v)", name, back, item, err)
		}
	}
}

func TestParseVaultItemRefusesWhatIsNotAnItem(t *testing.T) {
	otpauth := func(query string) string {
		return `{"type":"totp","totp":"otpauth://totp/Bank:ann?secret=` + vaultSeed + query + `"}`
	}
	for name, raw := range map[string]string{
		"not json":              `bank-password-0451`,
		"a list":                `[{"type":"note","note":"x"}]`,
		"unknown type":          `{"type":"card","note":"x"}`,
		"no type":               `{"password":"pw-0451"}`,
		"unknown field":         `{"type":"note","note":"x","cvv":"0451"}`,
		"two documents":         `{"type":"note","note":"x"}{"type":"note","note":"y"}`,
		"empty login":           `{"type":"login"}`,
		"empty note":            `{"type":"note"}`,
		"note with a password":  `{"type":"note","note":"x","password":"pw-0451"}`,
		"totp item without it":  `{"type":"totp"}`,
		"seed not base32":       `{"type":"totp","totp":"not-a-seed-0451!"}`,
		"seed too short":        `{"type":"totp","totp":"GEZDGNBV"}`,
		"unsupported algorithm": otpauth("&algorithm=MD5"),
		"nine digits":           otpauth("&digits=9"),
		"zero period":           otpauth("&period=0"),
		"not a totp uri":        `{"type":"totp","totp":"otpauth://hotp/Bank:ann?secret=` + vaultSeed + `"}`,
		"oversize":              `{"type":"note","note":"` + strings.Repeat("a", 70<<10) + `"}`,
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

func TestGeneratePasswordHonorsTheChosenClasses(t *testing.T) {
	const (
		lower   = "abcdefghijklmnopqrstuvwxyz"
		upper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		digits  = "0123456789"
		symbols = "!@#$%^&*()-_=+[]{}:,.?"
	)
	def := auth.DefaultPasswordOptions()
	if def.Length != 20 || !def.Lower || !def.Upper || !def.Digits || !def.Symbols {
		t.Fatalf("defaults = %+v, want 20 characters from all four classes", def)
	}
	seen := map[string]bool{}
	for i := 0; i < 300; i++ {
		pw, err := auth.GeneratePassword(def)
		if err != nil || len(pw) != 20 {
			t.Fatalf("GeneratePassword = %q, %v", pw, err)
		}
		for _, class := range []string{lower, upper, digits, symbols} {
			if !strings.ContainsAny(pw, class) {
				t.Fatalf("%q misses a chosen class (%q)", pw, class[:3])
			}
		}
		seen[pw] = true
	}
	if len(seen) < 300 {
		t.Fatalf("300 passwords gave %d distinct values", len(seen))
	}

	only := auth.PasswordOptions{Length: 128, Digits: true}
	got := map[rune]bool{}
	for i := 0; i < 50; i++ {
		pw, err := auth.GeneratePassword(only)
		if err != nil || len(pw) != 128 || strings.Trim(pw, digits) != "" {
			t.Fatalf("digits only = %q, %v", pw, err)
		}
		for _, r := range pw {
			got[r] = true
		}
	}
	if len(got) != 10 {
		t.Fatalf("6400 digits used %d of the 10 digits", len(got))
	}
	noSymbols := auth.PasswordOptions{Length: 64, Lower: true, Upper: true, Digits: true}
	if pw, err := auth.GeneratePassword(noSymbols); err != nil || strings.ContainsAny(pw, symbols) {
		t.Fatalf("symbols were not chosen but %q has some (%v)", pw, err)
	}
}

func TestGeneratePasswordRefusesWhatCannotBeSafe(t *testing.T) {
	for name, opts := range map[string]auth.PasswordOptions{
		"too short":  {Length: 7, Lower: true},
		"too long":   {Length: 129, Lower: true},
		"no classes": {Length: 20},
	} {
		if pw, err := auth.GeneratePassword(opts); err == nil || pw != "" {
			t.Errorf("%s: GeneratePassword = %q, %v, want an error and no password", name, pw, err)
		}
	}
}

func vaultStore(t *testing.T) *auth.SecretStore {
	t.Helper()
	return auth.NewSecretStore(testStore(t), secretTestSealer{})
}

// A vault item is sealed as its normalized document with the item's kind as the hint
// (a list shows what an item is without opening it), and a bad item is not stored.
func TestPutVaultSealsTheNormalizedDocument(t *testing.T) {
	svc := vaultStore(t)
	ctx := context.Background()
	tenant := model.TenantID("tenant-vault-2387")
	owner := model.NewID().String()
	name := "vault/" + owner + "/bank"

	view, err := svc.PutVault(ctx, adminActor(), tenant, name,
		auth.VaultItem{Type: "login", Username: "ann", Password: "pw-0451", TOTP: vaultSeed}, "Main account")
	if err != nil {
		t.Fatalf("PutVault: %v", err)
	}
	if view.Name != name || view.Hint != "login+totp" || view.Description != "Main account" {
		t.Fatalf("view = %+v", view)
	}
	value, err := svc.Reveal(ctx, adminActor(), tenant, name)
	want := `{"type":"login","username":"ann","password":"pw-0451","totp":"` + vaultSeed + `"}`
	if err != nil || string(value) != want {
		t.Fatalf("Reveal = %q, %v, want %q", value, err, want)
	}

	if _, err := svc.PutVault(ctx, adminActor(), tenant, "vault/"+owner+"/empty", auth.VaultItem{Type: "login"}, ""); err == nil {
		t.Error("an empty login was stored")
	}
	if _, err := svc.PutVault(ctx, adminActor(), tenant, "mcp/github", auth.VaultItem{Type: "note", Note: "x"}, ""); !errors.Is(err, auth.ErrBadSecretName) {
		t.Errorf("PutVault outside vault/ = %v, want ErrBadSecretName", err)
	}
	if _, err := svc.PutVault(ctx, adminActor(), auth.GlobalSecretScope, name, auth.VaultItem{Type: "note", Note: "x"}, ""); !errors.Is(err, auth.ErrBadSecretName) {
		t.Errorf("PutVault in the global scope = %v, want ErrBadSecretName", err)
	}
	if _, ok, _ := svc.Get(ctx, tenant, "vault/"+owner+"/empty"); ok {
		t.Error("the refused item exists")
	}
}

// The code is the current RFC 6238 code, the answer carries no seed, and a code is
// recorded on the ledger or not returned.
func TestVaultTOTPReturnsTheCodeAndNeverTheSeed(t *testing.T) {
	svc := vaultStore(t)
	ctx := context.Background()
	tenant := model.TenantID("tenant-vault-2387")
	name := "vault/" + model.NewID().String() + "/bank"
	if _, err := svc.PutVault(ctx, adminActor(), tenant, name, auth.VaultItem{Type: "totp", TOTP: vaultSeed}, ""); err != nil {
		t.Fatal(err)
	}

	// RFC 6238 Appendix B, T=59: 94287082 with eight digits, 287082 with six.
	code, err := svc.VaultTOTP(ctx, adminActor(), tenant, name, time.Unix(59, 0))
	want := auth.VaultCode{Code: "287082", SecondsLeft: 1, Period: 30, Digits: 6}
	if err != nil || code != want {
		t.Fatalf("VaultTOTP = %+v, %v, want %+v", code, err, want)
	}
	raw, _ := json.Marshal(code)
	if strings.Contains(string(raw), vaultSeed) {
		t.Fatalf("the answer carries the seed: %s", raw)
	}

	if _, err := svc.VaultTOTP(ctx, auth.Principal{Kind: auth.KindUser}, tenant, name, time.Unix(59, 0)); err == nil {
		t.Error("a code was returned to an actor the ledger cannot attribute")
	}
	if _, err := svc.VaultTOTP(ctx, adminActor(), tenant, "vault/"+model.NewID().String()+"/absent", time.Unix(59, 0)); !errors.Is(err, auth.ErrSecretNotFound) {
		t.Errorf("VaultTOTP of an absent item = %v, want ErrSecretNotFound", err)
	}
	note := "vault/" + model.NewID().String() + "/note"
	if _, err := svc.PutVault(ctx, adminActor(), tenant, note, auth.VaultItem{Type: "note", Note: "x"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VaultTOTP(ctx, adminActor(), tenant, note, time.Unix(59, 0)); !errors.Is(err, auth.ErrVaultNoTOTP) {
		t.Errorf("VaultTOTP of a note = %v, want ErrVaultNoTOTP", err)
	}
}
