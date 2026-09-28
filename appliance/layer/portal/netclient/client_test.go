// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package netclient

import (
	"context"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"os"
	"path/filepath"
	"testing"
)

func TestClient_AbsentOrForeignSocketDoesNotFallBack(t *testing.T) {
	c := Client{Socket: filepath.Join(t.TempDir(), "missing"), Owner: uint32(os.Getuid())}
	if _, err := c.Call(context.Background(), netguard.EdgeRequest{Action: "status"}); err == nil {
		t.Fatal("absent guard answered")
	}
	if err := os.WriteFile(c.Socket, []byte("not a socket"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(context.Background(), netguard.EdgeRequest{Action: "status"}); err == nil {
		t.Fatal("file accepted")
	}
}
