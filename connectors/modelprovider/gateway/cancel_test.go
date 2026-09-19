// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCreateMessageCancelClosesUpstream(t *testing.T) {
	for _, proto := range []Protocol{ProtocolOpenAICompat, ProtocolAnthropic, ProtocolOllama} {
		t.Run(string(proto), func(t *testing.T) {
			started := make(chan struct{})
			closed := make(chan struct{})
			srv := newFake(t, fakeCfg{protocol: proto, mode: fakeHang, started: started, closed: closed})
			hook := &recordingHook{}
			d := testDriver(t, proto, srv, hook)
			ctx, cancel := context.WithCancel(context.Background())
			errc := make(chan error, 1)
			go func() {
				_, err := d.CreateMessage(ctx, sampleReq())
				errc <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream did not see the request")
			}
			cancel()
			select {
			case err := <-errc:
				if err == nil {
					t.Fatal("canceled CreateMessage returned nil error")
				}
				if !errors.Is(err, context.Canceled) {
					var ge *Error
					if !errors.As(err, &ge) || ge.Code != CodeCanceled {
						t.Fatalf("want canceled, got %v", err)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("CreateMessage did not return after cancel")
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream request was not closed")
			}
			if hook.releases < 1 {
				t.Fatalf("Release not called after cancel (releases=%d)", hook.releases)
			}
			if hook.commits != 0 {
				t.Fatalf("Commit called on cancel (commits=%d)", hook.commits)
			}
		})
	}
}

func TestStreamMessageCancelClosesUpstream(t *testing.T) {
	for _, proto := range []Protocol{ProtocolOpenAICompat, ProtocolAnthropic, ProtocolOllama} {
		t.Run(string(proto), func(t *testing.T) {
			started := make(chan struct{})
			write2 := make(chan struct{})
			srv := newFake(t, fakeCfg{protocol: proto, started: started, write2: write2})
			hook := &recordingHook{}
			d := testDriver(t, proto, srv, hook)
			ctx, cancel := context.WithCancel(context.Background())
			st, err := d.StreamMessage(ctx, sampleReq())
			if err != nil {
				t.Fatalf("StreamMessage: %v", err)
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream did not see the stream")
			}
			if _, err := st.Recv(); err != nil {
				t.Fatalf("first Recv: %v", err)
			}
			cancel()
			errc := make(chan error, 1)
			go func() {
				_, err := st.Recv()
				errc <- err
			}()
			select {
			case err := <-errc:
				if err == nil {
					t.Fatal("canceled Recv returned nil")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Recv did not return after cancel")
			}
			_ = st.Close()
			if hook.commits != 0 {
				t.Fatalf("Commit on canceled stream (commits=%d)", hook.commits)
			}
			if hook.releases < 1 {
				t.Fatalf("Release not called (releases=%d)", hook.releases)
			}
		})
	}
}
