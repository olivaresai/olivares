// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localclient

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestLocalClient_MissingTLSDeliveryRefusesBeforeDial(t *testing.T) {
	for _, missing := range []string{"/etc/olivares-portal/tls.crt", "/etc/olivares-portal/tls.key", ""} {
		var paths []string
		dials := 0
		client, err := dialAfterTLS(func(path string) (os.FileInfo, error) {
			paths = append(paths, path)
			if path == missing {
				return nil, os.ErrNotExist
			}
			return nil, nil
		}, func() (*Client, error) { dials++; return &Client{}, nil })
		if !reflect.DeepEqual(paths, []string{"/etc/olivares-portal/tls.crt", "/etc/olivares-portal/tls.key"}) {
			t.Fatalf("metadata paths=%v", paths)
		}
		if missing == "" {
			if err != nil || client == nil || dials != 1 {
				t.Fatalf("present pair: client=%v dials=%d err=%v", client, dials, err)
			}
			continue
		}
		var failure *Failure
		if !errors.As(err, &failure) || failure.Code != "local_unavailable" || failure.Reason != "portal_tls_not_delivered" || failure.Sent || dials != 0 || client != nil {
			t.Fatalf("missing %s: failure=%#v dials=%d", missing, failure, dials)
		}
	}
}
