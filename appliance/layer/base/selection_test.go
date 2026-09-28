// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// productHost is an installation root with what generate-product-config needs: the product
// binary, the product configuration directory, the drop-in directory the base package ships
// and, when selectionDir is true, the Appliance Console directory the base package creates.
// Its runner stands in for the product's generator and for systemd.
func productHost(t *testing.T, selectionDir bool) Host {
	t.Helper()
	root := t.TempDir()
	place(t, root, productBinary, "#!/bin/sh\n")
	for _, dir := range []string{"/etc/olivares", filepath.Dir(productDropIn)} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if selectionDir {
		dir := filepath.Join(root, PortalSelectionDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == filepath.Join(root, productBinary) && len(args) > 1 && args[0] == "config" && args[1] == "generate":
			out := ""
			for i := range args[:len(args)-1] {
				if args[i] == "--out" {
					out = args[i+1]
				}
			}
			return nil, os.WriteFile(filepath.Join(root, out), []byte("# generated (profile: single-node-prod)\n"), 0o600)
		case name == "systemctl" && len(args) == 1 && args[0] == "daemon-reload":
			return nil, nil
		}
		return nil, errors.New("unexpected command " + name)
	}
	return Host{Root: root, Run: run}
}

// portalAnswers is the answers fixture with the Appliance Console fields: root is added
// beside "source" and host beside host.network, as the answers module's own tests add them.
func portalAnswers(t *testing.T, root, host string) Input {
	t.Helper()
	doc, err := os.ReadFile("../../answers/testdata/cloud-init.json")
	if err != nil {
		t.Fatal(err)
	}
	if root != "" {
		doc = bytes.Replace(doc, []byte(`"source": "file",`), []byte(`"source": "file", `+root+`,`), 1)
	}
	if host != "" {
		doc = bytes.Replace(doc, []byte(`"network": { "mode": "dhcp" },`), []byte(`"network": { "mode": "dhcp" }, `+host+`,`), 1)
	}
	in, err := NewInput("file:/etc/olivares-appliance/answers.json", "", doc)
	if err != nil {
		t.Fatalf("the answers were refused: %v", err)
	}
	return in
}

// publishedSelection is the file the Appliance Console reads, byte for byte, for the answers
// portal.enabled true, portal.listen management and host.management_interfaces [eth1, eth0]:
// the answers module's normalized values (the interfaces sorted), in a fixed order, with a
// terminal newline.
const publishedSelection = `{
  "schema_version": "olivares-portal-selection/v1",
  "portal": {
    "enabled": true,
    "listen": "management"
  },
  "host": {
    "management_interfaces": [
      "eth0",
      "eth1"
    ]
  }
}
`

func selectionPath(h Host) string { return filepath.Join(h.Root, PortalSelectionFile) }

func TestProductConfig_PublishesThePortalSelectionFromValidatedAnswers(t *testing.T) {
	in := portalAnswers(t, `"portal": {"enabled": true, "listen": "management"}`, `"management_interfaces": ["eth1", "eth0"]`)
	h := productHost(t, true)
	stage := ProductConfig{Host: h}

	effect, err := stage.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("generate-product-config refused answers that declare the console's selection: %v", err)
	}
	info, err := os.Lstat(selectionPath(h))
	if err != nil {
		t.Fatalf("no selection was published at %s: %v", PortalSelectionFile, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		t.Fatalf("the selection is %v, want a regular file with mode 0644 that the console's account can read", info.Mode())
	}
	got, err := os.ReadFile(selectionPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != publishedSelection {
		t.Fatalf("the published selection is\n%s\nwant the validated answers' view\n%s", got, publishedSelection)
	}
	if !strings.Contains(string(effect), "portal-selection=") {
		t.Fatalf("the recorded effect does not cover the selection it published: %q", effect)
	}
	if err := stage.Verify(context.Background(), in, effect); err != nil {
		t.Fatalf("the published selection does not verify against its record: %v", err)
	}

	t.Run("each field alone is published alone", func(t *testing.T) {
		for _, tc := range []struct{ root, host, want string }{
			{`"portal": {"enabled": false}`, "", `"enabled": false`},
			{`"portal": {"listen": "local"}`, "", `"listen": "local"`},
			{"", `"management_interfaces": ["enp1s0"]`, `"enp1s0"`},
		} {
			h := productHost(t, true)
			if _, err := (ProductConfig{Host: h}).Apply(context.Background(), portalAnswers(t, tc.root, tc.host)); err != nil {
				t.Fatalf("%s %s: %v", tc.root, tc.host, err)
			}
			got, err := os.ReadFile(selectionPath(h))
			if err != nil || !strings.Contains(string(got), tc.want) || strings.Count(string(got), "\n  \"") > 2 {
				t.Fatalf("%s %s: published %q (%v), want only %s beside the schema", tc.root, tc.host, got, err, tc.want)
			}
		}
	})

	t.Run("a selection changed after it was published does not verify", func(t *testing.T) {
		tampered := strings.Replace(publishedSelection, `"listen": "management"`, `"listen": "local"`, 1)
		if err := os.WriteFile(selectionPath(h), []byte(tampered), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := stage.Verify(context.Background(), in, effect); err == nil {
			t.Fatal("a selection that no longer says what the answers declare verified")
		}
		if err := os.WriteFile(selectionPath(h), []byte(publishedSelection), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(selectionPath(h), 0o664); err != nil {
			t.Fatal(err)
		}
		if err := stage.Verify(context.Background(), in, effect); err == nil {
			t.Fatal("a selection writable by its group verified")
		}
	})

	t.Run("without the directory the base package creates, the stage refuses and writes nothing", func(t *testing.T) {
		h := productHost(t, false)
		_, err := (ProductConfig{Host: h}).Apply(context.Background(), in)
		var outcome *Outcome
		if !errors.As(err, &outcome) || outcome.State != Refused {
			t.Fatalf("a missing %s was %v, want a refusal", PortalSelectionDir, err)
		}
		if _, err := os.Lstat(filepath.Join(h.Root, PortalSelectionDir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the stage created %s itself: %v", PortalSelectionDir, err)
		}
	})
}

func TestProductConfig_NoSelectionWhenPortalFieldsAreAbsent(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")

	t.Run("nothing is published and the effect keeps its earlier form", func(t *testing.T) {
		h := productHost(t, true)
		effect, err := (ProductConfig{Host: h}).Apply(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(selectionPath(h)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("answers without the console's fields published a selection: %v", err)
		}
		// A record written before the console's fields existed still verifies.
		if strings.Contains(string(effect), "portal-selection") || !strings.HasPrefix(string(effect), "olivares.env=") {
			t.Fatalf("the effect of answers without the console's fields changed form: %q", effect)
		}
	})

	t.Run("a selection left from earlier answers is removed", func(t *testing.T) {
		h := productHost(t, true)
		if err := os.WriteFile(selectionPath(h), []byte(publishedSelection), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := (ProductConfig{Host: h}).Apply(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(selectionPath(h)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a selection the answers no longer declare survived generate-product-config: %v", err)
		}
	})

	t.Run("a selection that appears later does not verify", func(t *testing.T) {
		h := productHost(t, true)
		stage := ProductConfig{Host: h}
		effect, err := stage.Apply(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(selectionPath(h), []byte(publishedSelection), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := stage.Verify(context.Background(), in, effect); err == nil {
			t.Fatal("a selection the answers do not declare verified")
		}
	})

	t.Run("the base package's directory is not required", func(t *testing.T) {
		if _, err := (ProductConfig{Host: productHost(t, false)}).Apply(context.Background(), in); err != nil {
			t.Fatalf("answers without the console's fields need no %s: %v", PortalSelectionDir, err)
		}
	})
}
