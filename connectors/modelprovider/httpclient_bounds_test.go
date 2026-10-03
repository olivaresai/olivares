// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type boundedBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *boundedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *boundedBody) Close() error { b.closed = true; return nil }

type boundedDoer struct{ body *boundedBody }

func (d boundedDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: d.body}, nil
}

func TestProviderJSONBoundsDecodeAndDiscard(t *testing.T) {
	const ceiling = 16 << 20
	for _, discard := range []bool{false, true} {
		for _, n := range []int{ceiling - 1, ceiling, ceiling + 1} {
			t.Run(fmt.Sprintf("discard-%t-size-%d", discard, n), func(t *testing.T) {
				body := &boundedBody{Reader: strings.NewReader(`{}` + strings.Repeat(" ", n-2))}
				c := NewClient("https://fixture.invalid", boundedDoer{body}, AuthNone, "", nil)
				var out any = new(json.RawMessage)
				if discard {
					out = nil
				}
				err := c.GetJSON(t.Context(), "/models", nil, out)
				if n > ceiling {
					if err == nil {
						t.Fatalf("discard=%t: accepted %d-byte body", discard, n)
					}
				} else if err != nil {
					t.Fatalf("discard=%t, size=%d: %v", discard, n, err)
				}
				if !body.closed {
					t.Fatal("response body was not closed")
				}
				if n <= ceiling && body.read != n {
					t.Fatalf("read only %d of %d allowed bytes", body.read, n)
				}
				if body.read > ceiling+1 {
					t.Fatalf("read %d bytes before refusing excess", body.read)
				}
			})
		}
	}
}

func TestProviderJSONBoundsOversizedNestedValue(t *testing.T) {
	body := &boundedBody{Reader: strings.NewReader(`{"nested":{"value":"` + strings.Repeat("x", 17<<20) + `"}}`)}
	c := NewClient("https://fixture.invalid", boundedDoer{body}, AuthBearer, "fixture-bearer", nil)
	if err := c.GetJSON(t.Context(), "/models", nil, new(json.RawMessage)); err == nil {
		t.Fatal("accepted oversized nested JSON")
	}
	if body.read > (16<<20)+1 || !body.closed {
		t.Fatalf("read=%d, closed=%t", body.read, body.closed)
	}
}

type interruptedJSON struct {
	err  error
	sent bool
}

func (r *interruptedJSON) Read(p []byte) (int, error) {
	if r.sent {
		return 0, r.err
	}
	r.sent = true
	return copy(p, `{} `), r.err
}
func TestProviderJSONBoundsPreserveReadFailure(t *testing.T) {
	failure := errors.New("fixture interrupted response")
	for _, out := range []any{nil, new(json.RawMessage)} {
		body := &boundedBody{Reader: &interruptedJSON{err: failure}}
		c := NewClient("https://fixture.invalid", boundedDoer{body}, AuthNone, "", nil)
		if err := c.GetJSON(t.Context(), "/models", nil, out); !errors.Is(err, failure) {
			t.Fatalf("read error = %v", err)
		}
		if !body.closed {
			t.Fatal("failed body was not closed")
		}
	}
}

type canceledJSON struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (r canceledJSON) Read([]byte) (int, error) { r.cancel(); return 0, r.ctx.Err() }
func TestProviderJSONBoundsPreserveCallerCancellation(t *testing.T) {
	for _, out := range []any{nil, new(json.RawMessage)} {
		ctx, cancel := context.WithCancel(t.Context())
		body := &boundedBody{Reader: canceledJSON{ctx, cancel}}
		c := NewClient("https://fixture.invalid", boundedDoer{body}, AuthNone, "", nil)
		err := c.GetJSON(ctx, "/models", nil, out)
		cancel()
		if !errors.Is(err, context.Canceled) || !body.closed {
			t.Fatalf("cancel error = %v, closed = %t", err, body.closed)
		}
	}
}

func TestProviderJSONBoundsAcceptRepresentativeCatalogs(t *testing.T) {
	for _, name := range []string{"../openai/testdata/models.json", "../gemini/testdata/models.json", "../local/testdata/ollama_tags.json", "../local/testdata/vllm_models.json", "../openrouter/testdata/models.json", "../codex/testdata/audit_logs.json"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			c := NewClient("https://fixture.invalid", &stubDoer{body: string(data)}, AuthNone, "", nil)
			if err := c.GetJSON(t.Context(), "/models", nil, new(json.RawMessage)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
