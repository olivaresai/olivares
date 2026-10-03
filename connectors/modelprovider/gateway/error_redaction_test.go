// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

func TestNativeStreamErrorRedactsReflectedHeader(t *testing.T) {
	const canary = "gateway-header-canary"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != canary {
			t.Error("credential was not sent")
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("rejected " + canary))
	}))
	defer s.Close()
	d := &httpDriver{protocol: ProtocolOllama, base: s.URL, path: "/fail", doer: s.Client(), extra: map[string]string{"X-API-Key": canary}}
	_, err := d.openStream(context.Background(), nil)
	var api *modelprovider.APIError
	if !errors.As(err, &api) || api.Status != http.StatusUnauthorized {
		t.Fatal("typed status lost")
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(api.Body, canary) {
		t.Fatal("gateway error retained synthetic secret")
	}
}
