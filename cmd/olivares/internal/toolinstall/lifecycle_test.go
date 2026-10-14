// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestRemoveReleaseRequiresAdmissionAndKeepsOtherVersions(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	r, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	f.publish(fxVersion2)
	other, _, err := f.install(fxVersion2)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := f.engine.InstalledVersion(t.Context(), f.root, "claude", fxVersion)
	if err != nil || selected.Executable != r.Destination.Executable || selected.SHA256 != r.Artifact.SHA256 {
		t.Fatalf("exact release = %+v %v", selected, err)
	}
	if _, err := f.engine.InstalledVersion(t.Context(), f.root, "claude", "latest"); err == nil {
		t.Fatal("channel admitted as a pin")
	}
	blocked := errors.New("tool_in_use: session running")
	_, err = f.engine.Remove(context.Background(), f.root, "claude", fxVersion, func(in Installed) (func(), error) {
		if in.SHA256 != r.Artifact.SHA256 {
			t.Fatal("removal did not identify verified bytes")
		}
		return nil, blocked
	})
	if !errors.Is(err, blocked) {
		t.Fatalf("in-use refusal = %v", err)
	}
	if _, err := os.Stat(r.Destination.Executable); err != nil {
		t.Fatal("refusal removed the binary", err)
	}
	released := false
	got, err := f.engine.Remove(context.Background(), f.root, "claude", fxVersion, func(in Installed) (func(), error) {
		return func() { released = true }, nil
	})
	if err != nil || got.Version != fxVersion || !released {
		t.Fatalf("remove = %+v %v, released=%v", got, err, released)
	}
	if _, err := os.Stat(r.Destination.Executable); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed binary remains: %v", err)
	}
	if _, err := os.Stat(other.Destination.Executable); err != nil {
		t.Fatal("other version removed", err)
	}
	for _, v := range []string{"", "..", "latest", fxVersion} {
		if _, err := f.engine.Remove(context.Background(), f.root, "claude", v, func(Installed) (func(), error) { return func() {}, nil }); err == nil {
			t.Fatalf("unsafe or absent version %q admitted", v)
		}
	}
}
