// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInstanceDecisionWaitCancels(t *testing.T) {
	m := New()
	release, err := m.lockInstance(context.Background(), "tenant/instance")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		unlock, err := m.lockInstance(ctx, "tenant/instance")
		if unlock != nil {
			unlock()
		}
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("conflicting decision did not wait: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled decision remained blocked")
	}
}
