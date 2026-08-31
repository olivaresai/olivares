// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ldap

import (
	"context"
	"reflect"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// guardedFieldName is the name the ST1003 exemption in ldap.go protects. It is a
// STRING on purpose: a rename sweep rewrites identifiers, so a sweep that
// "finishes the job" leaves this test compiling and FAILING instead of quietly
// following the rename.
//
// It is assembled from PIECES, also on purpose. A real sweep is a TEXTUAL
// substitution, and `sed s/privilegedDNs/privilegedDNS/g` over the package
// rewrites a whole string literal just as happily as an identifier. That would
// make the first assertion below pass VACUOUSLY and print a self-contradictory
// message ("rename it back to privilegedDNS"). Split like this, no substitution
// for the full name matches it, so both assertions stay load-bearing and the
// failure message keeps naming the right field.
//
// The constant is deliberately NOT called `privilegedDNsField`: that spelling
// trips the very ST1003 false positive this file exists to refuse, and a guard
// should not need a second exemption in order to hold the first one.
const guardedFieldName = "privileged" + "DNs"

// TestPrivilegedDNsAreDistinguishedNamesNotDNS is the guard behind the ST1003
// exemption on Source.privilegedDNs (ldap.go).
//
// staticcheck reads the "DNs" suffix as the DNS initialism and proposes
// `privilegedDNS`. The rename is a false positive with teeth: these are LDAP
// *Distinguished Names* (RFC 4514) — the operator-declared privileged GROUP
// identifiers the Gather grant scan matches on — so taking it turns an
// AUTHORIZATION concept into a NETWORK one for every future reader.
//
// The exemption ALONE would be an invitation. A later sweep would apply the
// rename, it would compile, and every behavioral test here would stay green:
// the field is unexported and the semantics do not change, so nothing else in
// this package can tell the difference. This test is the one thing that fails.
func TestPrivilegedDNsAreDistinguishedNamesNotDNS(t *testing.T) {
	st := reflect.TypeFor[Source]()

	if _, ok := st.FieldByName(guardedFieldName); !ok {
		t.Errorf("Source has no field %q. If this rename came from staticcheck ST1003, revert it: "+
			"DN here is an LDAP Distinguished Name (RFC 4514), not the Domain Name System. "+
			"See the exemption comment on the field in ldap.go.", guardedFieldName)
	}
	if _, ok := st.FieldByName("privilegedDNS"); ok {
		t.Errorf("Source has a field \"privilegedDNS\" (DNS, the Domain Name System). "+
			"This is the exact ST1003 false positive the exemption refuses: the field holds LDAP "+
			"Distinguished Names (RFC 4514) — group identifiers used for AUTHORIZATION, never network "+
			"names. Rename it back to %q.", guardedFieldName)
	}
}

// TestPrivilegedGroupDNsConfigKeyIsAdvertisedAndRead pins the OPERATOR-facing
// half of the same concept: the config key. Descriptor ADVERTISES the key an
// operator writes and Open READS it, and only ONE of those halves was covered.
// Renaming the key Open reads already reddened TestGatherEmitsPrivilegedGrants;
// renaming the key Descriptor ADVERTISES broke no test at all, while the engine
// went on reading the old one, so every deployed config would have silently
// stopped granting. This test is what closes that half. The key is documented in
// seven languages under docs-site/**/how-to/connectors/sso-scim-identity.md, so
// it cannot migrate either.
//
// It asserts through behavior (privilegedGroups), never through the field name,
// so it stays independent of the guard above.
func TestPrivilegedGroupDNsConfigKeyIsAdvertisedAndRead(t *testing.T) {
	const key = "privileged_group_dns"

	advertised := false
	for _, f := range New().Descriptor().ConfigFields {
		if f.Key == key {
			advertised = true
			break
		}
	}
	if !advertised {
		t.Errorf("Descriptor does not advertise config key %q; an operator has no documented way to "+
			"declare a privileged group DN. The key is published in seven languages under "+
			"docs-site/**/how-to/connectors/sso-scim-identity.md and cannot be renamed.", key)
	}

	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"url":     "ldap://directory.invalid:389",
		"base_dn": "dc=corp",
		// Mixed case on purpose: Open lowercases, so the match is case-insensitive.
		key: "CN=App Owners,OU=Groups,DC=corp",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	declared := entry("CN=App Owners,OU=Groups,DC=corp", map[string][]string{
		"objectClass": {"top", "group"}, "cn": {"App Owners"},
	})
	got := s.privilegedGroups([]*goldap.Entry{declared})
	if len(got) != 1 {
		t.Fatalf("privilegedGroups matched %d groups, want 1: the DN declared under %q never reached "+
			"the grant scan. Either Descriptor advertises a key Open does not read, or Open reads a key "+
			"the operator was never told to write.", len(got), key)
	}
	if got[0].mode != model.ModeUnknown {
		t.Errorf("operator-declared group mode = %v, want ModeUnknown: the connector must not guess "+
			"what a custom group grants", got[0].mode)
	}
}
