// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ldap

import (
	"context"
	"crypto/tls"
	"errors"
	"reflect"
	"strings"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// transportConn observes the existing external-connection seam. It does not
// simulate a certificate handshake or claim real-wire qualification.
type transportConn struct {
	directory *fakeConn
	events    []string
	configs   []*tls.Config
	tlsErr    error
}

func (c *transportConn) Bind(username, password string) error {
	c.events = append(c.events, "bind")
	return c.directory.Bind(username, password)
}

func (c *transportConn) StartTLS(cfg *tls.Config) error {
	c.events = append(c.events, "starttls")
	c.configs = append(c.configs, cfg.Clone())
	return c.tlsErr
}

func (c *transportConn) Search(req *goldap.SearchRequest) (*goldap.SearchResult, error) {
	c.events = append(c.events, "search")
	return c.directory.Search(req)
}

func (c *transportConn) Close() error {
	c.events = append(c.events, "close")
	return c.directory.Close()
}

func transportSource() (*Source, *transportConn) {
	s := New()
	c := &transportConn{directory: privDir()}
	s.dial = func(string) (Conn, error) {
		c.events = append(c.events, "dial")
		return c, nil
	}
	return s, c
}

func transportSettings(target string) map[string]string {
	return map[string]string{
		"url": target, "base_dn": "dc=corp", "bind_dn": "cn=transport-reader,dc=corp",
		"bind_password": testBindPassword,
	}
}

// On failure neither consumer may report a partial success. On success the
// existing roster and privileged-group fixture still produce their known output.
func consumeTransport(t *testing.T, s *Source, consumer string) error {
	t.Helper()
	if consumer == "snapshot" {
		graph, err := s.Snapshot(context.Background())
		if err != nil {
			if !reflect.DeepEqual(graph, identitysource.Graph{}) {
				t.Error("failed connection returned a partial graph")
			}
			return err
		}
		if len(graph.Identities) != 3 || len(graph.Collections) != 4 {
			t.Errorf("transport changed the directory graph: identities=%d groups=%d", len(graph.Identities), len(graph.Collections))
		}
		return nil
	}
	sink := &captureSink{}
	err := s.Gather(context.Background(), sink)
	if err != nil {
		if len(sink.obs) != 0 {
			t.Error("failed connection emitted partial observations")
		}
		return err
	}
	origins := map[string]bool{}
	findings := 0
	for _, observation := range sink.obs {
		switch value := observation.(type) {
		case model.EdgeObservation:
			if value.ResourceRef != "dc=corp" || value.Mode != model.ModeReadWrite || value.ToolRef != dnDA {
				t.Errorf("transport changed privileged grant semantics: %+v", value)
			}
			origins[value.OriginRef] = true
		case model.FindingReport:
			findings++
		default:
			t.Errorf("unexpected observation type %T", observation)
		}
	}
	if len(sink.obs) != 3 || findings != 1 || !reflect.DeepEqual(origins, map[string]bool{dnAlice: true, dnSvc: true}) {
		t.Errorf("transport changed privileged grant output: %v", sink.obs)
	}
	return nil
}

func assertSafeTransportRejection(t *testing.T, err error, settings map[string]string) {
	t.Helper()
	if err == nil {
		t.Error("unsafe transport was accepted")
		return
	}
	if len(err.Error()) > 200 {
		t.Error("validation diagnostic is not bounded")
	}
	for _, sensitive := range []string{settings["url"], settings["bind_dn"], settings["bind_password"], "url-user", "url-password"} {
		if sensitive != "" && strings.Contains(err.Error(), sensitive) {
			t.Error("validation diagnostic exposed a configuration value")
		}
	}
}

func TestTransportRejectsUnsafeBindBeforeDial(t *testing.T) {
	for _, skipVerify := range []string{"false", "true"} {
		t.Run("skip_verify_"+skipVerify, func(t *testing.T) {
			s, conn := transportSource()
			settings := transportSettings("ldap://directory.invalid:389")
			settings["insecure_skip_verify"] = skipVerify
			assertSafeTransportRejection(t, s.Open(context.Background(), sdk.Config{Settings: settings}), settings)
			// Deliberately call after failed Open: connect owns the guard too.
			for _, consumer := range []string{"snapshot", "gather"} {
				assertSafeTransportRejection(t, consumeTransport(t, s, consumer), settings)
			}
			got, err := s.connect()
			assertSafeTransportRejection(t, err, settings)
			if got != nil || len(conn.events) != 0 {
				t.Errorf("unsafe bind reached the connection seam: %v", conn.events)
			}
		})
	}
}

func TestTransportRejectsInvalidEndpoints(t *testing.T) {
	for _, target := range []string{
		"http://directory.invalid", "cldap://directory.invalid", "ldapi:///tmp/ldap.socket",
		"directory.invalid", "ldap:directory.invalid", "ldaps://", "ldap:///dc=corp", "ldaps://:636",
		"ldaps://directory.invalid:", "ldaps://directory.invalid:0", "ldaps://directory.invalid:65536",
		"ldaps://directory.invalid:bad", "ldaps://%zz", "ldap://[2001:db8::1", "ldap://2001:db8::1",
		"ldap://[not-an-ip]:389", "ldaps://url-user:url-password@directory.invalid",
		"ldap://url-user@directory.invalid",
	} {
		t.Run(target, func(t *testing.T) {
			s, conn := transportSource()
			settings := transportSettings(target)
			settings["start_tls"] = "true"
			assertSafeTransportRejection(t, s.Open(context.Background(), sdk.Config{Settings: settings}), settings)
			for _, consumer := range []string{"snapshot", "gather"} {
				assertSafeTransportRejection(t, consumeTransport(t, s, consumer), settings)
			}
			if len(conn.events) != 0 {
				t.Errorf("invalid endpoint reached the connection seam: %v", conn.events)
			}
		})
	}
}

func TestTransportStartTLSOrdersVerifiedBind(t *testing.T) {
	for _, endpoint := range []struct{ target, hostname string }{
		{"ldap://directory.invalid:389", "directory.invalid"},
		{"ldap://[2001:db8::1]:389", "2001:db8::1"},
		{"ldap://directory.invalid", "directory.invalid"},
	} {
		for _, consumer := range []string{"snapshot", "gather"} {
			t.Run(endpoint.hostname+"/"+consumer, func(t *testing.T) {
				s, conn := transportSource()
				settings := transportSettings(endpoint.target)
				settings["start_tls"] = "true"
				if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
					t.Fatal(err)
				}
				if err := consumeTransport(t, s, consumer); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(conn.events, []string{"dial", "starttls", "bind", "search", "search", "close"}) {
					t.Errorf("TLS must precede credential bind and searches: %v", conn.events)
				}
				if len(conn.configs) != 1 {
					t.Fatalf("StartTLS config count = %d", len(conn.configs))
				}
				cfg := conn.configs[0]
				if cfg.ServerName != endpoint.hostname || cfg.MinVersion < tls.VersionTLS12 || cfg.InsecureSkipVerify {
					t.Errorf("verified StartTLS requires hostname=%q, TLS >=1.2 and verification: name=%q min=%d skip=%v",
						endpoint.hostname, cfg.ServerName, cfg.MinVersion, cfg.InsecureSkipVerify)
				}
			})
		}
	}
}

func TestTransportStartTLSFailureClosesBeforeBind(t *testing.T) {
	for _, consumer := range []string{"snapshot", "gather"} {
		t.Run(consumer, func(t *testing.T) {
			s, conn := transportSource()
			conn.tlsErr = errors.New("TLS handshake refused")
			settings := transportSettings("ldap://directory.invalid:389")
			settings["start_tls"] = "true"
			if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
				t.Fatal(err)
			}
			if err := consumeTransport(t, s, consumer); !errors.Is(err, conn.tlsErr) {
				t.Errorf("TLS refusal must remain a failure: %v", err)
			}
			if !reflect.DeepEqual(conn.events, []string{"dial", "starttls", "close"}) {
				t.Errorf("TLS failure must close without bind/search/retry: %v", conn.events)
			}
		})
	}
}

func TestTransportLDAPSDoesNotRestartTLS(t *testing.T) {
	for _, startTLS := range []string{"false", "true"} {
		for _, consumer := range []string{"snapshot", "gather"} {
			t.Run(startTLS+"/"+consumer, func(t *testing.T) {
				s, conn := transportSource()
				settings := transportSettings("ldaps://directory.invalid:636")
				settings["start_tls"] = startTLS
				if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
					t.Fatal(err)
				}
				if err := consumeTransport(t, s, consumer); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(conn.events, []string{"dial", "bind", "search", "search", "close"}) || len(conn.configs) != 0 {
					t.Errorf("ldaps must use the already protected connection: %v", conn.events)
				}
			})
		}
	}
}

func TestTransportAnonymousAndOfflineRemainUsable(t *testing.T) {
	for _, consumer := range []string{"snapshot", "gather"} {
		t.Run("anonymous/"+consumer, func(t *testing.T) {
			s, conn := transportSource()
			if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
				"url": "ldap://directory.invalid:389", "base_dn": "dc=corp",
			}}); err != nil {
				t.Fatal(err)
			}
			if err := consumeTransport(t, s, consumer); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(conn.events, []string{"dial", "search", "search", "close"}) {
				t.Errorf("anonymous LDAP must remain unbound: %v", conn.events)
			}
		})
	}
	t.Run("offline", func(t *testing.T) {
		s, conn := transportSource()
		if err := s.Open(context.Background(), sdk.Config{Settings: transportSettings("")}); err != nil {
			t.Fatal(err)
		}
		graph, err := s.Snapshot(context.Background())
		if err != nil || len(graph.Identities) != 0 || len(graph.Collections) != 0 || len(graph.Memberships) != 0 {
			t.Fatalf("offline Snapshot changed: %v", err)
		}
		sink := &captureSink{}
		if err := s.Gather(context.Background(), sink); err != nil || len(sink.obs) != 0 || len(conn.events) != 0 {
			t.Fatalf("offline connector must have no effects: err=%v events=%v", err, conn.events)
		}
	})
}

func TestTransportExplicitSkipVerifyStillUsesTLS(t *testing.T) {
	s, conn := transportSource()
	settings := transportSettings("ldap://directory.invalid:389")
	settings["start_tls"], settings["insecure_skip_verify"] = "true", "true"
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatal(err)
	}
	if err := consumeTransport(t, s, "snapshot"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(conn.events, []string{"dial", "starttls", "bind", "search", "search", "close"}) {
		t.Fatalf("skip-verify must still establish TLS before bind: %v", conn.events)
	}
	if len(conn.configs) != 1 || !conn.configs[0].InsecureSkipVerify || conn.configs[0].ServerName != "directory.invalid" || conn.configs[0].MinVersion < tls.VersionTLS12 {
		t.Fatal("explicit existing TLS verification override was not retained")
	}
}
