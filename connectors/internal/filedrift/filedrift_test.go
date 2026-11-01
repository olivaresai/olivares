// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package filedrift

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

var (
	at   = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	spec = Spec{Origin: "test_managed_config", Tool: "Test", SetArrays: []string{"allow"}}
)

func titles(fs []model.FindingReport) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.Title + "\n")
	}
	return b.String()
}

func TestJSONDrift(t *testing.T) {
	authored := `{"permission":{"edit":"deny","bash":"ask"},"share":"disabled","allow":["b","a"]}`
	tests := []struct {
		name     string
		observed string
		want     []string // substrings of the finding titles, in path order
		sev      model.Severity
	}{
		{"identical", authored, nil, ""},
		{"extra observed keys and a reordered allowlist are not drift",
			`{"model":"x","allow":["a","b"],"share":"disabled","permission":{"bash":"ask","edit":"deny","extra":1}}`, nil, ""},
		{"jsonc comments are tolerated",
			"{\n// managed\n\"permission\":{\"edit\":\"deny\",\"bash\":\"ask\",},\"share\":\"disabled\",\"allow\":[\"a\",\"b\"]}", nil, ""},
		{"value changed", `{"permission":{"edit":"allow","bash":"ask"},"share":"disabled","allow":["a","b"]}`,
			[]string{"permission.edit differs"}, model.SeverityHigh},
		{"key missing", `{"permission":{"edit":"deny"},"share":"disabled","allow":["a","b"]}`,
			[]string{"permission.bash is NOT set"}, model.SeverityHigh},
		{"array broadened", `{"permission":{"edit":"deny","bash":"ask"},"share":"disabled","allow":["a","b","c"]}`,
			[]string{"allow differs"}, model.SeverityHigh},
		{"file absent", "  \n", []string{"is absent on the host"}, model.SeverityHigh},
		{"file invalid", `{"permission":`, []string{"is present but invalid"}, model.SeverityHigh},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := JSON(spec, "host-1", []byte(authored), []byte(tt.observed), at)
			if err != nil {
				t.Fatalf("JSON: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("findings = %d, want %d:\n%s", len(got), len(tt.want), titles(got))
			}
			for i, f := range got {
				if !strings.Contains(f.Title, tt.want[i]) {
					t.Errorf("finding %d title %q, want it to contain %q", i, f.Title, tt.want[i])
				}
				if f.Severity != tt.sev || f.Kind != "policy_drift" || f.SubjectKind != "test_managed_config" || f.SubjectRef != "host-1" || f.DetailHash == "" || !f.OccurredAt.Equal(at) {
					t.Errorf("finding %d = %+v", i, f)
				}
				if strings.Contains(f.Title, "deny") || strings.Contains(f.Title, "allow\"") {
					t.Errorf("finding %d title leaks a value: %q", i, f.Title)
				}
			}
		})
	}
}

func TestJSONAuthoredInvalid(t *testing.T) {
	if _, err := JSON(spec, "h", []byte(`[1]`), []byte(`{}`), at); err == nil {
		t.Fatal("a non-object authored document must be an error, not a pass")
	}
	if _, err := JSON(spec, "h", []byte(`{"a":`), []byte(`{}`), at); err == nil {
		t.Fatal("a malformed authored document must be an error")
	}
}

func TestTOMLDrift(t *testing.T) {
	authored := "[sandbox]\nprofile = \"strict\"\n\n[mcp_servers.a]\n\n[mcp_servers.b]\n"
	got, err := TOML(spec, "h", []byte(authored), []byte(authored), at)
	if err != nil || len(got) != 0 {
		t.Fatalf("identical TOML: err=%v findings=%s", err, titles(got))
	}
	got, err = TOML(spec, "h", []byte(authored), []byte("[sandbox]\nprofile = \"off\"\n[mcp_servers.a]\n"), at)
	if err != nil {
		t.Fatal(err)
	}
	want := "mcp_servers.b is NOT set|sandbox.profile differs"
	if g := strings.TrimSpace(strings.ReplaceAll(titles(got), "Test: ", "")); !strings.Contains(strings.ReplaceAll(g, "\n", "|"), "mcp_servers.b is NOT set") || !strings.Contains(g, "sandbox.profile differs") {
		t.Fatalf("TOML drift titles = %q, want %q", g, want)
	}
	got, _ = TOML(spec, "h", []byte(authored), []byte("not = = toml"), at)
	if len(got) != 1 || !strings.Contains(got[0].Title, "is present but invalid") {
		t.Fatalf("invalid observed TOML = %s", titles(got))
	}
}

func TestTOMLExactTable(t *testing.T) {
	sp := Spec{Origin: "test_managed_config", Tool: "Test", ExactTables: []string{"mcp_servers"}}
	authored := "[mcp_servers.a]\n"
	got, err := TOML(sp, "h", []byte(authored), []byte("[mcp_servers.a]\n[mcp_servers.rogue]\n[other]\nx = 1\n"), at)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Test: mcp_servers.rogue is on the host but not in the published policy" {
		t.Fatalf("extra allowlist entry = %s", titles(got))
	}
	if got, _ := TOML(sp, "h", []byte(authored), []byte("[mcp_servers.a]\n[other]\nx = 1\n"), at); len(got) != 0 {
		t.Fatalf("a key outside the exact table is the host's own: %s", titles(got))
	}
	// Without the option the same host file is clean: extras are the host's own.
	if got, _ := TOML(spec, "h", []byte(authored), []byte("[mcp_servers.a]\n[mcp_servers.rogue]\n"), at); len(got) != 0 {
		t.Fatalf("default compare flagged an extra key: %s", titles(got))
	}
}

func TestArraysCompareInOrderUnlessTheyAreAllowlists(t *testing.T) {
	authored := `{"command":["npx","-y","pkg"],"allow":["a","b"]}`
	got, err := JSON(spec, "h", []byte(authored), []byte(`{"command":["pkg","-y","npx"],"allow":["b","a"]}`), at)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Title, "command differs") {
		t.Fatalf("a reordered argv is drift, a reordered allowlist is not: err=%v %s", err, titles(got))
	}
	if got, _ := JSON(spec, "h", []byte(`{"allow":["a","a"]}`), []byte(`{"allow":["a","b"]}`), at); len(got) != 1 {
		t.Fatalf("an allowlist compares as a multiset, not a set: %s", titles(got))
	}
}

func TestTheRealNestingWinsOverAFlatKeyThatSpellsThePath(t *testing.T) {
	authored := `{"permission":{"bash":"deny"}}`
	decoy := `{"permission.bash":"deny","permission":{"bash":"allow"}}`
	got, err := JSON(spec, "h", []byte(authored), []byte(decoy), at)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Title, "permission.bash differs") {
		t.Fatalf("a decoy flat key must not hide a weaker nested value: err=%v %s", err, titles(got))
	}
	// A key that really contains a dot is still found.
	dotted := `{"mcp":{"a.b":{"type":"local"}}}`
	if got, _ := JSON(spec, "h", []byte(dotted), []byte(dotted), at); len(got) != 0 {
		t.Fatalf("a dotted key compared against itself: %s", titles(got))
	}
	if got, _ := JSON(spec, "h", []byte(dotted), []byte(`{"mcp":{}}`), at); len(got) != 1 {
		t.Fatalf("a missing dotted key: %s", titles(got))
	}
}

func TestTypeMismatchAndEmptyValuesAreDrift(t *testing.T) {
	for name, tc := range map[string]struct{ authored, observed string }{
		"scalar published, table observed": {`{"share":"disabled"}`, `{"share":{"mode":"disabled"}}`},
		"table published, scalar observed": {`{"permission":{"edit":"deny"}}`, `{"permission":"deny"}`},
		"empty array published":            {`{"allow":[]}`, `{"allow":["a"]}`},
		"number against string":            {`{"timeout":30}`, `{"timeout":"30"}`},
	} {
		got, err := JSON(spec, "h", []byte(tc.authored), []byte(tc.observed), at)
		if err != nil || len(got) == 0 {
			t.Errorf("%s: err=%v findings=%d, want drift", name, err, len(got))
		}
	}
}

func TestAnAuthoredDocumentWithTrailingDataIsRefused(t *testing.T) {
	for _, doc := range []string{`{"a":1} junk`, `{"a":1}{"b":2}`} {
		if _, err := JSON(spec, "h", []byte(doc), []byte(`{}`), at); err == nil {
			t.Errorf("%q: trailing data after the object must be an error", doc)
		}
	}
}

func TestTOMLNotANumberIsNeverReportedClean(t *testing.T) {
	got, err := TOML(spec, "h", []byte("allow = [nan]\n"), []byte("allow = [inf]\n"), at)
	if err != nil || len(got) == 0 {
		t.Fatalf("nan against inf must be drift: err=%v %s", err, titles(got))
	}
}

// The tools read a managed file by nesting; a flat key that merely spells a nested path is
// inert on the host and must not satisfy the published key.
func TestAFlatKeyThatSpellsANestedPathDoesNotSatisfyIt(t *testing.T) {
	authored := `{"permission":{"bash":"deny"}}`
	for _, observed := range []string{
		`{"permission.bash":"deny"}`,
		`{"permission":{"edit":"allow"},"permission.bash":"deny"}`,
	} {
		got, err := JSON(spec, "h", []byte(authored), []byte(observed), at)
		if err != nil || len(got) != 1 || !strings.Contains(got[0].Title, "permission.bash is NOT set") {
			t.Errorf("%s: err=%v %s; want permission.bash NOT set", observed, err, titles(got))
		}
	}
	if got, _ := TOML(spec, "h", []byte("[permission]\nbash = \"deny\"\n"), []byte("\"permission.bash\" = \"deny\"\n"), at); len(got) != 1 {
		t.Errorf("a quoted TOML key that spells the path: %s", titles(got))
	}
}

// A file that decodes to nothing (JSON null) is not a managed file.
func TestAHostFileThatIsJSONNullIsUngoverned(t *testing.T) {
	got, err := JSON(spec, "h", []byte(`{}`), []byte(`null`), at)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Title, "present but invalid") {
		t.Fatalf("null host file: err=%v %s", err, titles(got))
	}
}

// A host file is evidence the plane did not author: it cannot make one check-in raise
// an unbounded number of findings.
func TestTheFindingsOfOneComparisonAreBounded(t *testing.T) {
	sp := Spec{Origin: "test_managed_config", Tool: "Test", ExactTables: []string{"mcp_servers"}}
	var host strings.Builder
	host.WriteString("[mcp_servers.a]\n")
	for i := 0; i < 5*MaxFindings; i++ {
		fmt.Fprintf(&host, "[mcp_servers.rogue%d]\n", i)
	}
	got, err := TOML(sp, "h", []byte("[mcp_servers.a]\n"), []byte(host.String()), at)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxFindings+1 || !strings.Contains(got[len(got)-1].Title, "more differences are not listed") {
		t.Fatalf("findings = %d, last %q; want %d plus a summary", len(got), got[len(got)-1].Title, MaxFindings)
	}
}

// An empty published allowlist is a lockdown: the host may keep the table but no entry.
func TestAnEmptyPublishedAllowlistIsALockdown(t *testing.T) {
	sp := Spec{Origin: "test_managed_config", Tool: "Test", ExactTables: []string{"mcp_servers"}}
	for host, want := range map[string]int{
		"[mcp_servers.rogue]\n": 1, // an entry the policy does not allow
		"other = 1\n":           1, // no table at all: not the published lockdown
		"[mcp_servers]\n":       0, // the lockdown, as published
	} {
		got, err := TOML(sp, "h", []byte("[mcp_servers]\n"), []byte(host), at)
		if err != nil || len(got) != want {
			t.Errorf("host %q: err=%v findings=%d, want %d: %s", host, err, len(got), want, titles(got))
		}
	}
}

// Two different facts on one host are two findings the plane can tell apart; the same fact
// found twice is the same finding. A title never carries a value of either document.
func TestFindingsAreDistinctStableAndCarryNoValues(t *testing.T) {
	authored := `{"permission":{"edit":"SECRET-AUTHORED","bash":"AUTHORED-BASH"},"share":"AUTHORED-SHARE"}`
	observed := `{"permission":{"edit":"SECRET-OBSERVED"}}`
	a, err := JSON(spec, "h", []byte(authored), []byte(observed), at)
	if err != nil || len(a) != 3 { // edit differs, bash and share not set
		t.Fatalf("err=%v %s", err, titles(a))
	}
	b, _ := JSON(spec, "h", []byte(authored), []byte(observed), at)
	seen := map[string]bool{}
	for i, f := range a {
		if f.DetailHash != b[i].DetailHash {
			t.Errorf("finding %q has an unstable hash", f.Title)
		}
		if seen[f.DetailHash] {
			t.Errorf("two findings share the hash %s: %s", f.DetailHash, titles(a))
		}
		seen[f.DetailHash] = true
		for _, v := range []string{"SECRET", "AUTHORED", "OBSERVED"} {
			if strings.Contains(f.Title, v) {
				t.Errorf("title %q carries a document value", f.Title)
			}
		}
	}
	other, _ := JSON(spec, "h2", []byte(authored), []byte(observed), at)
	if other[0].DetailHash == a[0].DetailHash {
		t.Error("the same fact on two hosts must be two findings")
	}
}

func TestJSONExactTablesUnderAWildcard(t *testing.T) {
	sp := Spec{Origin: "test_managed_config", Tool: "Test", ExactTables: []string{"perm.*"}}
	authored := `{"perm":{"bash":{"*":"deny"},"a.b":{"x":"ask"},"edit":"ask"}}`
	got, err := JSON(sp, "h", []byte(authored), []byte(`{"perm":{"bash":{"*":"deny","rm *":"allow"},"a.b":{"x":"ask","y":"allow"},"edit":"ask","new":"allow"}}`), at)
	if err != nil {
		t.Fatal(err)
	}
	want := "Test: perm.a.b.y is on the host but not in the published policy\nTest: perm.bash.rm * is on the host but not in the published policy\n"
	if g := titles(got); g != want {
		t.Fatalf("entries added under each published table = %q, want %q", g, want)
	}
	if got, _ := JSON(sp, "h", []byte(authored), []byte(authored), at); len(got) != 0 {
		t.Fatalf("the published file on the host: %s", titles(got))
	}
	token := "gh" + "p_" + strings.Repeat("A", 36) // built at run time: no key-shaped literal in Git
	observed := `{"perm":{"bash":{"*":"deny","curl -H 'Authorization: token ` + token + `' *":"allow"},"a.b":{"x":"ask"},"edit":"ask"}}`
	got, _ = JSON(sp, "h", []byte(authored), []byte(observed), at)
	if len(got) != 1 || strings.Contains(got[0].Title, token) || !strings.Contains(got[0].Title, "[REDACTED:github-token]") {
		t.Fatalf("a host key carrying a credential must be scrubbed from the title: %s", titles(got))
	}
}

func TestJSONPatternTablesKeepHostTextOutOfTitles(t *testing.T) {
	sp := Spec{Origin: "test_managed_config", Tool: "Test", ExactTables: []string{"perm", "perm.*"}, PatternTables: []string{"perm.*"}}
	authored := `{"perm":{"bash":{"*":"deny","psql -W hunter3 *":"ask"},"edit":"ask"}}`
	curlAuth := "curl -" + "u admin:hunter2 *" // built at run time: no credential-shaped literal in Git
	observed := `{"perm":{"bash":{"*":"allow","` + curlAuth + `":"allow","mysql -pS3cr3tPass *":"allow"},"edit":"ask","webfetch":"allow"}}`
	got, err := JSON(sp, "h", []byte(authored), []byte(observed), at)
	if err != nil {
		t.Fatal(err)
	}
	entry := func(pattern string) string { return "Test: perm.bash entry " + redact.Hash(pattern)[:12] }
	want := "Test: perm.webfetch is on the host but not in the published policy\n" +
		entry(curlAuth) + " is on the host but not in the published policy\n" +
		entry("mysql -pS3cr3tPass *") + " is on the host but not in the published policy\n" +
		entry("*") + " differs from the published policy\n" +
		entry("psql -W hunter3 *") + " is NOT set on the host\n"
	if g := titles(got); g != want {
		t.Fatalf("titles = %q, want %q", g, want)
	}
	seen := map[string]bool{}
	for _, f := range got {
		for _, secret := range []string{"hunter2", "hunter3", "S3cr3tPass"} {
			if strings.Contains(f.Title, secret) {
				t.Errorf("title %q carries pattern text", f.Title)
			}
		}
		if seen[f.DetailHash] {
			t.Errorf("two entries share one DetailHash: %q", f.Title)
		}
		seen[f.DetailHash] = true
	}
}
