// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
)

// Item 8: EVERY post-transmit-ambiguous outcome (the remote MAY have acted)
// must be ErrAfterTransmit so the governed caller settles UNKNOWN, never a false
// "failed" that a re-approval could turn into a DUPLICATE effect. Genuinely
// pre-transmit failures (connection refused) stay definite.
func TestS467PostTransmitAmbiguousClassification(t *testing.T) {
	cases := []struct {
		name string
		rpc  []byte
		code int
	}{
		{"5xx after receive", rpcTask("TASK_STATE_SUBMITTED"), 502},
		{"malformed response body", []byte(`not json at all`), 200},
		{"rpc error after processing", []byte(`{"jsonrpc":"2.0","id":"1","error":{"code":-32001,"message":"boom"}}`), 200},
		{"undecodable accepted result", []byte(`{"jsonrpc":"2.0","id":"1","result":123}`), 200},
		// A 2xx whose result carries neither a task status nor a message role ({},
		// {"foo":"bar"}) is non-conformant → ambiguous, not a fabricated success.
		{"unrecognized result shape", []byte(`{"jsonrpc":"2.0","id":"1","result":{}}`), 200},
		{"garbage result object", []byte(`{"jsonrpc":"2.0","id":"1","result":{"foo":"bar"}}`), 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, doer, spec := newVerifiedTestClient(t, tc.rpc)
			doer.rpcStatus = tc.code
			_, err := c.SendMessageCapable(context.Background(), spec)
			if err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
			if !errors.Is(err, ErrAfterTransmit) {
				t.Fatalf("%s: err=%v, want ErrAfterTransmit (ambiguous ⇒ unknown, not failed)", tc.name, err)
			}
		})
	}
}

// A genuinely PRE-transmit failure (dial connection refused — no bytes left) must
// NOT be ambiguous: it is a clean, definite failure.
func TestS467PreTransmitStaysDefinite(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	card := signedCardBytes(t, priv, "k1", baseCard("summarizer"))
	inner := &stubDoer{cardBytes: card, rpcBytes: rpcTask("TASK_STATE_SUBMITTED")}
	c := NewClient(EmitConfig{TrustJWKS: jwks, Doer: dialErrDoer{inner: inner}})
	_, err := c.SendMessageCapable(context.Background(),
		SendSpec{AgentName: "summarizer", AgentURL: "https://summarizer.example.com", Text: "x", Skill: "summarize"})
	if err == nil {
		t.Fatal("expected a dial failure")
	}
	if errors.Is(err, ErrAfterTransmit) {
		t.Fatalf("a pre-transmit dial refusal must be a DEFINITE failure, not ambiguous: %v", err)
	}
}

// Item 8: the default client must NOT auto-follow redirects. A followed 3xx
// (POST to A → redirect to a dead B → dial failure on B) would surface B's dial
// error, which isPreTransmit misreads as a DEFINITE pre-transmit failure even
// though the POST to A left — a false "failed" a re-approval could duplicate.
func TestS467DefaultClientDoesNotFollowRedirects(t *testing.T) {
	c := NewClient(EmitConfig{})
	hc, ok := c.doer.(*http.Client)
	if !ok {
		t.Fatalf("default doer is %T, want *http.Client", c.doer)
	}
	if hc.CheckRedirect == nil {
		t.Fatal("default a2a client must set CheckRedirect to not follow redirects")
	}
	if err := hc.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect = %v, want http.ErrUseLastResponse (redirects not followed)", err)
	}
}

// dialErrDoer serves the card (GET) so trust verifies, then fails the POST with a
// dial-phase connection error (nothing transmitted).
type dialErrDoer struct{ inner *stubDoer }

func (d dialErrDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost {
		return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	}
	return d.inner.Do(req)
}
