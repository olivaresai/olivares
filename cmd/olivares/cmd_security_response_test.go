// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/secadvisory"
)

func runSecResp(args ...string) (string, error) {
	cmd := newSecurityCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

const draftAdvisoryFeed = `{
  "author": "Olivares PSIRT",
  "advisories": [
    {
      "id": "GHSA-test-0001",
      "summary": "example control-plane vuln",
      "severity": [{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}],
      "affected": [{
        "package": {"ecosystem": "Go", "name": "github.com/olivaresai/olivares/cmd/olivares"},
        "ranges": [{"type": "SEMVER", "events": [{"introduced": "0"}, {"fixed": "26.7.1"}]}]
      }]
    }
  ]
}`

// TestSecurityAdvisoriesProducer proves the PSIRT producer builds a feed that verifies
// under the release key and is consumable by the same reader the product runs.
func TestSecurityAdvisoriesProducer(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	draft := filepath.Join(dir, "draft.json")
	if err := os.WriteFile(draft, []byte(draftAdvisoryFeed), 0o600); err != nil {
		t.Fatal(err)
	}
	feed := filepath.Join(dir, "advisories.json")

	if out, err := runSecResp("advisories", "--in", draft, "--out", feed, "--sign-key", base64.StdEncoding.EncodeToString(priv),
		"--expect-pubkey", base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatalf("advisories: %v\n%s", err, out)
	}
	fb, err := os.ReadFile(feed)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := os.ReadFile(feed + ".sig")
	if err != nil {
		t.Fatalf("feed signature not written: %v", err)
	}
	// The produced feed verifies against the release key and parses under the product reader.
	f, err := secadvisory.VerifyFeed(fb, sg, pub)
	if err != nil {
		t.Fatalf("produced feed did not verify: %v", err)
	}
	if len(f.Advisories) != 1 || f.Advisories[0].ID != "GHSA-test-0001" {
		t.Fatalf("produced feed content wrong: %+v", f)
	}
	// A different key must be refused (fail-closed).
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := secadvisory.VerifyFeed(fb, sg, otherPub); err == nil {
		t.Fatal("feed must not verify under an untrusted key")
	}
}

const draftRulePackGraft = `{
  "version": 4,
  "blocked_mcp": ["evil-mcp"],
  "indicators": [{"type": "domain", "value": "bad.example", "severity": "HIGH"}],
  "patterns": [{"id": "inj-1", "match": "ignore previous instructions"}]
}`

// TestSecurityRulePackGraftCLI is the offline author→verify loop for a hot-reload
// rule-pack (the integration graft).
func TestSecurityRulePackGraftCLI(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	draft := filepath.Join(dir, "draft.json")
	_ = os.WriteFile(draft, []byte(draftRulePackGraft), 0o600)
	pack := filepath.Join(dir, "rulepack.json")

	if out, err := runSecResp("rulepack", "sign", "--in", draft, "--out", pack, "--sign-key", base64.StdEncoding.EncodeToString(priv),
		"--expect-pubkey", base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatalf("rulepack sign: %v\n%s", err, out)
	}
	out, err := runSecResp("rulepack", "verify", "--in", pack, "--pubkey", base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatalf("rulepack verify: %v\n%s", err, out)
	}
	if !strings.Contains(out, "v4") || !strings.Contains(out, "1 blocked MCP") {
		t.Fatalf("verify summary unexpected:\n%s", out)
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := runSecResp("rulepack", "verify", "--in", pack, "--pubkey", base64.StdEncoding.EncodeToString(otherPub)); err == nil {
		t.Fatal("verify with an untrusted key must fail")
	}
}
