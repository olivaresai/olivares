//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package identitytest checks the Community connector inventory contract.
package identitytest

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
)

// Source is the observation and inventory contract a connector exposes.
type Source interface {
	sdk.SourceConnector
	identitysource.GraphProvider
}

// AssertUnavailable rejects silent empty success, including configured sources.
func AssertUnavailable(t *testing.T, newSource func() Source) {
	t.Helper()
	for _, tc := range []struct {
		name       string
		configured bool
	}{{"offline", false}, {"configured", true}} {
		t.Run(tc.name, func(t *testing.T) {
			src := newSource()
			if tc.configured {
				err := src.Open(context.Background(), sdk.Config{Settings: map[string]string{
					"path": "testdata", "entries_file": "testdata/entries.json", "base_url": "https://fixture.invalid",
					"token": "fixture-token", "admin_key": "fixture-admin", "access_token": "fixture-token", "org_id": "fixture-org",
				}})
				if err != nil && !strings.Contains(err.Error(), "Business") {
					t.Fatalf("configure source: %v", err)
				}
			}
			graph, err := src.Snapshot(context.Background())
			if err == nil || !strings.Contains(err.Error(), "Business") {
				t.Fatalf("inventory must report Business requirement, got graph=%+v err=%v", graph, err)
			}
			if !reflect.DeepEqual(graph, identitysource.Graph{}) {
				t.Fatalf("unavailable inventory leaked a partial graph: %+v", graph)
			}
		})
	}
}
