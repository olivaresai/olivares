// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
)

// vaultImportSeedLink is the otpauth link the fixtures carry; its secret is the RFC 6238 SHA-1 test seed.
const vaultImportSeedLink = "otpauth://totp/%s?secret=" + vaultCLISeed

func vaultImportFixture(t *testing.T, source string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "vault-import", source+".csv"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type wantImported struct {
	label, description, kind string
	item                     auth.VaultItem
}

// Each export layout reads into the items its rows mean, and what it cannot read is
// counted, never guessed at or silently dropped.
func TestParseVaultImportReadsEverySource(t *testing.T) {
	seedLink := func(who string) string { return strings.Replace(vaultImportSeedLink, "%s", who, 1) }
	for _, c := range []struct {
		source      string
		file        string
		items       []wantImported
		failed      map[int]string // row -> a word its reason must contain
		unsupported map[string]int
		dropped     map[string]int
	}{{
		source: "bitwarden", file: "bitwarden",
		items: []wantImported{
			{"Bank", "", "login", auth.VaultItem{Type: "login", Username: "ann", Password: "pw-bw-bank-2388", URL: "https://bank.example",
				Note: "Main account\nsecond line", TOTP: seedLink("Bank:ann") + "&issuer=Bank"}},
			{"Forum-old", "Forum (old)", "login", auth.VaultItem{Type: "login", Username: "bob", Password: "pw-bw-forum-2388", URL: "https://forum.example"}},
			{"Safe-code", "Safe code", "note", auth.VaultItem{Type: "note", Note: "the code is 4411-bw-2388"}},
			{"Forum-old-2", "Forum (old)", "login", auth.VaultItem{Type: "login", Username: "carol", Password: "pw-bw-forum2-2388", URL: "https://forum2.example"}},
		},
		failed:      map[int]string{7: "base32"},
		unsupported: map[string]int{"card": 1},
		dropped:     map[string]int{"folder": 1, "custom fields": 1, "extra websites": 1},
	}, {
		source: "keepass", file: "keepass",
		items: []wantImported{
			{"Mail", "", "login", auth.VaultItem{Type: "login", Username: "ann@mail.example", Password: "pw-kp-mail-2388", URL: "https://mail.example"}},
			{"Backup-2FA", "Backup 2FA", "totp", auth.VaultItem{Type: "totp", Note: "kept offline", TOTP: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"}},
			{"Wi-Fi", "", "login", auth.VaultItem{Type: "login", Password: "pw-kp-wifi-2388", Note: "guest network"}},
		},
		failed:      map[int]string{5: "title"},
		unsupported: map[string]int{},
		dropped:     map[string]int{"group": 3},
	}, {
		source: "1password", file: "1password",
		items: []wantImported{
			{"Shop", "", "login", auth.VaultItem{Type: "login", Username: "ann", Password: "pw-op-shop-2388", URL: "https://shop.example",
				TOTP: seedLink("Shop:ann") + "&period=30&digits=6"}},
			{"Router", "", "login", auth.VaultItem{Type: "login", Username: "admin", Password: "pw-op-router-2388", URL: "https://192.168.0.1", Note: "admin page, not the Wi-Fi"}},
			{"Recovery-codes", "Recovery codes", "note", auth.VaultItem{Type: "note", Note: "A1B2-C3D4-op-2388"}},
		},
		failed:      map[int]string{},
		unsupported: map[string]int{},
		dropped:     map[string]int{"tags": 1},
	}, {
		source: "chrome", file: "chrome",
		items: []wantImported{
			{"news.example", "", "login", auth.VaultItem{Type: "login", Username: "ann", Password: "pw-ch-news-2388", URL: "https://news.example/login"}},
			{"item", "映画", "login", auth.VaultItem{Type: "login", Username: "ann", Password: "pw-ch-movies-2388", URL: "https://movies.example", Note: `likes, and "quotes"`}},
		},
		failed:      map[int]string{4: "username or a password"},
		unsupported: map[string]int{},
		dropped:     map[string]int{},
	}, {
		source: "proton", file: "proton",
		items: []wantImported{
			{"Cloud", "", "login", auth.VaultItem{Type: "login", Username: "ann@mail.example", Password: "pw-pp-cloud-2388", URL: "https://cloud.example",
				TOTP: seedLink("Cloud:ann")}},
			{"Games", "", "login", auth.VaultItem{Type: "login", Username: "annie", Password: "pw-pp-games-2388", URL: "https://games.example"}},
			{"Passport", "", "note", auth.VaultItem{Type: "note", Note: "number is pp-2388"}},
		},
		failed:      map[int]string{},
		unsupported: map[string]int{"alias": 1, "creditcard": 1},
		dropped:     map[string]int{"email": 1, "vault": 3},
	}} {
		t.Run(c.source, func(t *testing.T) {
			got, err := parseVaultImport(c.source, vaultImportFixture(t, c.file))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(got.Items) != len(c.items) {
				t.Fatalf("items = %d, want %d: %+v", len(got.Items), len(c.items), got.Items)
			}
			for i, want := range c.items {
				it := got.Items[i]
				if it.Label != want.label || it.Description != want.description || it.Item != want.item || it.Item.Type != want.kind {
					t.Errorf("item %d = {%q %q %+v}, want {%q %q %+v}", i, it.Label, it.Description, it.Item, want.label, want.description, want.item)
				}
				if msg := auth.ValidateSecretName(it.Label); msg != "" {
					t.Errorf("item %d label %q is not a valid name: %s", i, it.Label, msg)
				}
			}
			if len(got.Failed) != len(c.failed) {
				t.Fatalf("failed rows = %+v, want rows %v", got.Failed, c.failed)
			}
			for _, f := range got.Failed {
				word, ok := c.failed[f.Row]
				if !ok || !strings.Contains(f.Reason, word) {
					t.Errorf("failed row %d reason %q, want one of %v", f.Row, f.Reason, c.failed)
				}
				// A reason names the field at fault and never repeats what the row held.
				if strings.Contains(f.Reason, "2388") || strings.Contains(f.Reason, "!!") {
					t.Errorf("failed row %d reason repeats a value: %q", f.Row, f.Reason)
				}
			}
			if !reflect.DeepEqual(nonNil(got.Unsupported), c.unsupported) {
				t.Errorf("unsupported = %v, want %v", got.Unsupported, c.unsupported)
			}
			if !reflect.DeepEqual(nonNil(got.Dropped), c.dropped) {
				t.Errorf("dropped = %v, want %v", got.Dropped, c.dropped)
			}
		})
	}
}

func nonNil(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}

// A file that is not the layout it was said to be, or a layout nobody knows, is a wrong
// invocation: exit 2 before any row is read, and the message repeats nothing of the file.
func TestParseVaultImportRefusesTheWrongFile(t *testing.T) {
	for what, c := range map[string]struct {
		source string
		data   string
	}{
		"an unknown source":       {"lastpass", "name,password\nx,pw-wrong-2388\n"},
		"no password column":      {"bitwarden", "name,notes\nx,pw-wrong-2388\n"},
		"no title column":         {"keepass", "Password,Notes\npw-wrong-2388,x\n"},
		"an empty file":           {"chrome", ""},
		"a quote that never ends": {"chrome", "name,url,username,password\n\"x,y,z,pw-wrong-2388\n"},
	} {
		_, err := parseVaultImport(c.source, []byte(c.data))
		if exitcode.From(err) != exitcode.Usage || strings.Contains(err.Error(), "pw-wrong-2388") {
			t.Errorf("%s: err=%v (exit %d), want exit %d repeating nothing of the file", what, err, exitcode.From(err), exitcode.Usage)
		}
	}
	// A header with no rows is a valid, empty import.
	if got, err := parseVaultImport("chrome", []byte("name,url,username,password,note\n")); err != nil || len(got.Items) != 0 {
		t.Errorf("a header and no rows: %+v, %v, want an empty import", got, err)
	}
}

// A byte order mark and mixed-case headers, as spreadsheets save them, read the same.
func TestParseVaultImportToleratesABOMAndHeaderCase(t *testing.T) {
	got, err := parseVaultImport("chrome", []byte("\ufeffName, URL ,USERNAME,Password\nx,https://x.example,ann,pw-bom-2388\n"))
	if err != nil || len(got.Items) != 1 || got.Items[0].Item.Password != "pw-bom-2388" || got.Items[0].Label != "x" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

// The labels of an import never collide with each other and are always valid names,
// whatever the titles were.
func TestVaultImportLabelsAreValidAndUnique(t *testing.T) {
	titles := []string{"Bank", "Bank", "bank", "a/b", "a b", "a  b", "...", "日本語", "x" + strings.Repeat("y", 300), " padded "}
	var csv strings.Builder
	csv.WriteString("name,url,username,password\n")
	for _, title := range titles {
		csv.WriteString("\"" + title + "\",https://x.example,ann,pw-label-2388\n")
	}
	got, err := parseVaultImport("chrome", []byte(csv.String()))
	if err != nil || len(got.Items) != len(titles) {
		t.Fatalf("items = %d err %v, want %d", len(got.Items), err, len(titles))
	}
	seen := map[string]bool{}
	for i, it := range got.Items {
		if msg := auth.ValidateSecretName(it.Label); msg != "" || len(it.Label) > 200 {
			t.Errorf("title %q -> label %q: %s", titles[i], it.Label, msg)
		}
		if seen[it.Label] {
			t.Errorf("title %q -> label %q collides", titles[i], it.Label)
		}
		seen[it.Label] = true
	}
	if got.Items[0].Label != "Bank" || got.Items[1].Label != "Bank-2" {
		t.Errorf("a repeated title gets a counter: %q %q", got.Items[0].Label, got.Items[1].Label)
	}
}

// A password, a note and a title come through the CSV reader exactly: quotes, commas, a newline
// and spaces at the ends of a password are data, and a title too long for a description is cut
// at a character, never in the middle of one.
func TestParseVaultImportKeepsPasswordsAndTitlesExactly(t *testing.T) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.UseCRLF = true // the file a Windows spreadsheet writes
	password := " x,y\n\"z\"\ttab "
	long := strings.Repeat("映", 600)
	_ = w.WriteAll([][]string{
		{"name", "url", "username", "password", "note"},
		{"a", "https://a.example", "ann", password, "two\nlines"},
		{long, "https://b.example", "bob", "pw-long-2388", ""},
		{"short", "https://c.example", "cat"}, // a row with fewer cells than the header
	})
	got, err := parseVaultImport("chrome", buf.Bytes())
	if err != nil || len(got.Items) != 3 || len(got.Failed) != 0 {
		t.Fatalf("parse: %v %+v", err, got)
	}
	if it := got.Items[0].Item; it.Password != password || it.Note != "two\nlines" {
		t.Errorf("password %q note %q did not come through exactly", it.Password, it.Note)
	}
	desc := got.Items[1].Description
	if got.Items[1].Label != "item" || len(desc) > 1024 || !utf8.ValidString(desc) || auth.ValidateVaultDescription(desc) != nil {
		t.Errorf("long title: label %q, description %d bytes valid=%v", got.Items[1].Label, len(desc), utf8.ValidString(desc))
	}
	if it := got.Items[2].Item; it.Username != "cat" || it.Password != "" {
		t.Errorf("a short row = %+v, want only the username", it)
	}
}

// What a file puts in the report is words of ours: an odd type cell is "other", a control
// character in a title becomes a space, and the login fields a note row has no place for are
// counted rather than lost without a word.
func TestParseVaultImportKeepsHostileTextOutOfTheReport(t *testing.T) {
	csv := "type,name,notes,login_password\n" +
		"\"\x1b]52;c;aGk=\x07\",a,,x\n" +
		strings.Repeat("y", 500) + ",b,,x\n" +
		"login,\"ti\ntle\x1b[2J\",,pw-ctl-2388\n" +
		"note,n,text,pw-in-a-note-2388\n"
	got, err := parseVaultImport("bitwarden", []byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Unsupported, map[string]int{"other": 2}) {
		t.Errorf("unsupported = %v, want the two odd kinds as other", got.Unsupported)
	}
	if len(got.Items) != 2 || strings.ContainsAny(got.Items[0].Description, "\n\x1b") || got.Items[0].Description == "" {
		t.Errorf("items = %+v, want the title kept as a description without control characters", got.Items)
	}
	if got.Items[1].Item.Type != "note" || got.Items[1].Item.Password != "" || got.Dropped["login fields of a note"] != 1 {
		t.Errorf("note row = %+v dropped %v, want a note and its password counted", got.Items[1].Item, got.Dropped)
	}
}

// A file that holds more than a vault can is refused as soon as it passes the limit, and a
// thousand repeats of one title are numbered without a quadratic search.
func TestParseVaultImportRefusesMoreThanAVaultHolds(t *testing.T) {
	var csv strings.Builder
	csv.WriteString("name,url,username,password\n")
	for i := 0; i <= auth.MaxVaultItemsPerOwner; i++ {
		csv.WriteString("t" + strings.Repeat("x", i) + ",https://x.example,ann,pw\n")
	}
	if _, err := parseVaultImport("chrome", []byte(csv.String())); exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "500") {
		t.Errorf("501 items: err=%v, want exit %d naming 500", err, exitcode.Err)
	}
	var same strings.Builder
	same.WriteString("name,url,username,password\n")
	for i := 0; i < auth.MaxVaultItemsPerOwner; i++ {
		same.WriteString("Bank,https://x.example,ann,pw\n")
	}
	got, err := parseVaultImport("chrome", []byte(same.String()))
	if err != nil || len(got.Items) != auth.MaxVaultItemsPerOwner || got.Items[len(got.Items)-1].Label != "Bank-500" {
		t.Fatalf("500 repeats: err=%v items=%d", err, len(got.Items))
	}
}

// An Olivares export comes back with the names it went out under, whatever `vault put` let
// them be, and a file that was edited into something the vault cannot hold fails by row.
func TestParseVaultExportKeepsLabelsAndChecksRows(t *testing.T) {
	doc := func(items string) []byte { return []byte(`{"format":"olivares-vault","items":[` + items + `]}`) }
	login := `{"label":"%s","type":"login","username":"ann","password":"pw-x-2388"}`
	label := func(l string) string { return strings.Replace(login, "%s", l, 1) }
	longest := strings.Repeat("l", 213)
	got, err := parseVaultImport("olivares", doc(strings.Join([]string{
		label("work/bank"), label(".env"), label("_priv"), label("a-b"), label(longest),
		`{"label":"both","description":"Main, \"joint\"","type":"login","password":"pw-y-2388","totp":"GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"}`,
	}, ",")))
	if err != nil || len(got.Items) != 6 || len(got.Failed) != 0 {
		t.Fatalf("parse: %v %+v", err, got)
	}
	for i, want := range []string{"work/bank", ".env", "_priv", "a-b", longest, "both"} {
		if got.Items[i].Label != want {
			t.Errorf("item %d label = %q, want %q verbatim", i, got.Items[i].Label, want)
		}
	}
	if it := got.Items[5]; it.Description != `Main, "joint"` || it.Item.TOTP == "" || it.Item.Password != "pw-y-2388" {
		t.Errorf("a login with an authenticator key = %+v", it)
	}

	bad := doc(strings.Join([]string{
		label("ok"), label("ok"), label(" padded"), label("has space"), label(strings.Repeat("l", 214)), label(""),
		`{"label":"empty","type":"login"}`,
		`{"label":"long","description":"` + strings.Repeat("d", 1025) + `","type":"login","password":"pw-z-2388"}`,
	}, ","))
	got, err = parseVaultImport("olivares", bad)
	rows := map[int]string{}
	for _, f := range got.Failed {
		rows[f.Row] = f.Reason
	}
	if err != nil || len(got.Items) != 1 || len(rows) != 7 || rows[2] != "the label appears twice" || strings.Contains(strings.Join(mapValues(rows), "|"), "2388") {
		t.Errorf("edited export: err=%v items=%d failed=%v", err, len(got.Items), rows)
	}

	for what, data := range map[string]string{
		"another format":        `{"format":"other","items":[]}`,
		"no format":             `{"items":[]}`,
		"an unknown field":      `{"format":"olivares-vault","items":[],"extra":1}`,
		"text after it":         `{"format":"olivares-vault","items":[]} {}`,
		"an unknown item field": `{"format":"olivares-vault","items":[{"label":"a","type":"login","password":"pw-w-2388","pin":"1"}]}`,
		"not JSON":              "label,password\nx,pw-w-2388\n",
	} {
		if _, err := parseVaultImport("olivares", []byte(data)); exitcode.From(err) != exitcode.Usage || strings.Contains(err.Error(), "2388") {
			t.Errorf("%s: err=%v (exit %d), want exit %d repeating nothing", what, err, exitcode.From(err), exitcode.Usage)
		}
	}
}

func mapValues(m map[int]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
