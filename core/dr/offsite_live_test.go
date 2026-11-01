// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// cutUploadConn closes a real TCP connection after part of the request body has
// crossed it. Unlike an error returned before dialing, this exercises server-side
// handling of a truncated Content-Length upload.
type cutUploadConn struct {
	net.Conn
	remaining int
	sent      *atomic.Int64
}

func (c *cutUploadConn) Write(p []byte) (int, error) {
	if len(p) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.Conn.Write(p)
	c.remaining -= n
	c.sent.Add(int64(n))
	if c.remaining == 0 {
		_ = c.Conn.Close()
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func TestOffsiteLiveInterruptedUpload(t *testing.T) {
	endpoint := os.Getenv("OLIVARES_TEST_OFFSITE_ENDPOINT")
	if endpoint == "" {
		t.Skip("run scripts/dr-offsite-targets.sh for real-bucket qualification")
	}
	readCredential := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(os.Getenv(name))
		if err != nil {
			t.Fatalf("read credential reference %s: %v", name, err)
		}
		return strings.TrimSpace(string(b))
	}
	cfg := OffsiteConfig{Endpoint: endpoint, Bucket: "dr-qualification", Prefix: "interrupt",
		AccessKeyID:     readCredential("OLIVARES_TEST_OFFSITE_ACCESS_FILE"),
		SecretAccessKey: readCredential("OLIVARES_TEST_OFFSITE_SECRET_FILE"), Timeout: 10 * time.Second}
	cli, err := NewOffsiteClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("overwrite=%t", overwrite), func(t *testing.T) {
			name := fmt.Sprintf("interrupted-%t.drbundle", overwrite)
			original := []byte("previous complete object")
			if overwrite {
				if err := cli.Put(ctx, name, bytes.NewReader(original), int64(len(original))); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				if err := cli.Delete(ctx, name); err != nil {
					t.Errorf("cleanup object: %v", err)
				}
			})
			broken, err := NewOffsiteClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var sent atomic.Int64
			transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				upstream, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				return &cutUploadConn{Conn: upstream, remaining: 64 * 1024, sent: &sent}, nil
			}}
			defer transport.CloseIdleConnections()
			broken.http.Transport = transport
			body := bytes.Repeat([]byte("x"), 1024*1024)
			if err := broken.Put(ctx, name, bytes.NewReader(body), int64(len(body))); err == nil {
				t.Fatal("truncated upload reported success")
			}
			if sent.Load() != 64*1024 {
				t.Fatal("upload did not reach the mid-body TCP cut")
			}
			// Probe through an intact connection. Authentication/network errors are
			// failures, never evidence that an object was absent.
			r, err := cli.Get(ctx, name)
			if overwrite {
				if err != nil {
					t.Fatal(err)
				}
				got, readErr := io.ReadAll(r)
				closeErr := r.Close()
				if readErr != nil || closeErr != nil || !bytes.Equal(got, original) {
					t.Fatalf("interrupted overwrite damaged previous object: read=%v close=%v", readErr, closeErr)
				}
			} else if err == nil {
				_ = r.Close()
				t.Fatal("partial object visible under final name")
			} else if !strings.Contains(err.Error(), "returned HTTP 404: NoSuchKey") {
				t.Fatalf("expected NoSuchKey after interrupted new upload: %v", err)
			}
			objects, err := cli.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, object := range objects {
				if object.Name == name && (!overwrite || object.Size != int64(len(original))) {
					t.Fatal("list exposed the partial upload")
				}
			}
		})
	}
}
