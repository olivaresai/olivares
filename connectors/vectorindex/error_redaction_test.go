// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vectorindex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVectorProviderErrorRedactsReflectedCredential(t *testing.T) {
	const canary = "vector-opaque-canary"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") != canary && r.Header.Get("Authorization") != "Bearer "+canary {
			t.Error("credential not sent")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("rejected " + canary))
	}))
	defer s.Close()
	calls := map[string]func(context.Context, string, string, any, any) error{
		"qdrant":   (&qdrant{base: s.URL, apiKey: canary, doer: s.Client()}).call,
		"pinecone": (&pinecone{host: s.URL, apiKey: canary, doer: s.Client()}).call,
		"milvus":   (&milvus{base: s.URL, apiKey: canary, doer: s.Client()}).call,
		"weaviate": (&weaviate{base: s.URL, apiKey: canary, doer: s.Client()}).call,
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call(context.Background(), http.MethodPost, "/fixture", nil, nil)
			if err == nil || !strings.Contains(err.Error(), "403") {
				t.Fatal("HTTP rejection lost")
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatal("vector error retained credential")
			}
		})
	}
}
