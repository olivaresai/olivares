// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package answers_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/answers"
)

const staticNetwork = `{"mode":"static","interfaces":[{"name":"ens4","ipv4":{"addresses":["192.0.2.10/24"],"gateway":"192.0.2.1"},"ipv6":{"addresses":["2001:db8::10/64"],"gateway":"2001:db8::1"}}],"dns":{"servers":["192.0.2.53","2001:db8::53"],"search":["example.test"]}}`

func networkDocument(t *testing.T, network string) string {
	t.Helper()
	return strings.Replace(fixture(t), `{ "mode": "dhcp" }`, network, 1)
}

func TestAnswersV1_StaticNetworkValidatesAndRefusesConflicts(t *testing.T) {
	valid := networkDocument(t, staticNetwork)
	if _, err := answers.Build(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"wrong family", "192.0.2.10/24", "2001:db8::10/64"},
		{"gateway outside prefix", "192.0.2.1", "198.51.100.1"},
		{"duplicate address", `"192.0.2.10/24"`, `"192.0.2.10/24","192.0.2.10/24"`},
		{"loopback", "192.0.2.10/24", "127.0.0.1/8"},
		{"unspecified", "2001:db8::10/64", "::/64"},
		{"mapped", "2001:db8::10/64", "::ffff:192.0.2.10/120"},
		{"default prefix", "192.0.2.10/24", "192.0.2.10/0"},
		{"interface", "ens4", "../ens4"},
		{"conflicting mode", `"mode":"static"`, `"mode":"dhcp"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := answers.Build(strings.NewReader(strings.Replace(valid, tc.from, tc.to, 1))); err == nil {
				t.Fatal("conflicting static settings accepted")
			}
		})
	}
}

func TestAnswersV1_DnsFieldsAreOptionalAndUnknownFieldsRefused(t *testing.T) {
	for _, network := range []string{`{"mode":"dhcp"}`, `{"mode":"dhcp","dns":{"servers":["192.0.2.53"],"search":["example.test"]}}`} {
		if _, err := answers.Build(strings.NewReader(networkDocument(t, network))); err != nil {
			t.Fatal(err)
		}
	}
	for _, dns := range []string{`{"servers":["DO-NOT-PRINT"]}`, `{"servers":["127.0.0.1"]}`, `{"servers":["192.0.2.53","192.0.2.53"]}`, `{"search":["a.test","A.test"]}`, `{"secret":"DO-NOT-PRINT"}`, `{}`} {
		_, err := answers.Build(strings.NewReader(networkDocument(t, `{"mode":"dhcp","dns":`+dns+`}`)))
		if err == nil || strings.Contains(err.Error(), "DO-NOT-PRINT") {
			t.Fatalf("unsafe DNS acceptance/error: %v", err)
		}
	}
}

func TestAnswersV1_DocumentWithoutNetworkAdditionsPlansAsBefore(t *testing.T) {
	p, err := answers.Build(strings.NewReader(fixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Answers struct {
			Host struct {
				Network map[string]any `json:"network"`
			} `json:"host"`
		} `json:"answers"`
		Operations []struct{ Owner, Action, State string } `json:"operations"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Answers.Host.Network, map[string]any{"mode": "dhcp"}) {
		t.Fatal("optional fields changed existing plan")
	}
	want := []struct{ Owner, Action, State string }{{"cloud-init", "wait-for-completion", "pending"}, {"cloud-init", "verify-host-settings", "pending"}, {"olivares", "generate-product-config", "pending"}, {"olivares", "initialize-storage", "pending"}}
	if !reflect.DeepEqual(got.Operations, want) {
		t.Fatalf("existing plan operations changed: %+v", got.Operations)
	}
}

func TestAnswersV1_DnsPriorityAndSearchOrderArePreserved(t *testing.T) {
	p, err := answers.Build(strings.NewReader(networkDocument(t, `{"mode":"dhcp","dns":{"servers":["192.0.2.54","192.0.2.53"],"search":["z.test","a.test"]}}`)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Answers struct {
			Host struct {
				Network struct {
					DNS struct{ Servers, Search []string }
				}
			}
		}
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Answers.Host.Network.DNS.Servers, []string{"192.0.2.54", "192.0.2.53"}) || !reflect.DeepEqual(v.Answers.Host.Network.DNS.Search, []string{"z.test", "a.test"}) {
		t.Fatal("DNS precedence changed")
	}
}
