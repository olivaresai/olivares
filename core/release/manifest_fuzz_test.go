// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import "testing"

// FuzzParseManifest exercises the OTA/release manifest decoder on hostile input.
// ParseManifest decodes an UNTRUSTED update manifest (the shape check that runs
// before signature verification), so it must never panic and must never accept a
// manifest that violates the shape invariants it promises: a manifest that parses
// with a nil error has a supported schema version and a valid channel.
func FuzzParseManifest(f *testing.F) {
	f.Add([]byte(``))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"schema_version":1,"channel":"stable","version":"1.0.0","artifacts":[]}`))
	f.Add([]byte(`{"schema_version":999,"channel":"stable"}`))
	f.Add([]byte(`{"schema_version":1,"channel":"../../etc/passwd","version":"1.0.0"}`))
	f.Add([]byte(`{"schema_version":1,"channel":"stable","version":"not-a-semver"}`))

	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := ParseManifest(b)
		if err != nil {
			return // a rejected manifest is the expected outcome for hostile bytes
		}
		// On the success path ParseManifest guarantees these; a regression that
		// weakened the shape checks would let a bad manifest through here.
		if m.SchemaVersion != ManifestSchemaVersion {
			t.Fatalf("accepted manifest with schema_version %d (want %d)", m.SchemaVersion, ManifestSchemaVersion)
		}
		if !ValidChannel(m.Channel) {
			t.Fatalf("accepted manifest with invalid channel %q", m.Channel)
		}
	})
}
