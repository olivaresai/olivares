// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package answers_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/answers"
)

func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/cloud-init.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCloudInitPlanOwnsNoHostEffects(t *testing.T) {
	p, err := answers.Build(strings.NewReader(fixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Schema     string                           `json:"schema_version"`
		State      string                           `json:"state"`
		Executable bool                             `json:"executable"`
		Operations []struct{ Owner, Action string } `json:"operations"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Schema != "appliance-plan/v1" || got.State != "planned" || got.Executable {
		t.Fatalf("not an explicitly non-executable plan: %s", b)
	}
	if len(got.Operations) != 4 {
		t.Fatalf("missing plan stages: %s", b)
	}
	if got.Operations[0].Owner != "cloud-init" || got.Operations[0].Action != "wait-for-completion" || got.Operations[1].Owner != "cloud-init" || got.Operations[1].Action != "verify-host-settings" {
		t.Fatalf("cloud-init ownership lost: %s", b)
	}
	if got.Operations[2].Owner != "olivares" || got.Operations[2].Action != "generate-product-config" || got.Operations[3].Action != "initialize-storage" {
		t.Fatalf("product intentions missing: %s", b)
	}
	for _, op := range got.Operations {
		if op.Owner == "cloud-init" && strings.HasPrefix(op.Action, "apply") {
			t.Fatal("host effects leaked into plan")
		}
	}
}

func reject(t *testing.T, input, path string) {
	t.Helper()
	p, err := answers.Build(strings.NewReader(input))
	if err == nil {
		t.Fatal("invalid answers returned a plan")
	}
	if !strings.Contains(err.Error(), path+":") {
		t.Fatalf("error should name %s: %v", path, err)
	}
	if strings.Contains(err.Error(), "DO-NOT-PRINT") {
		t.Fatalf("input value escaped: %v", err)
	}
	if b, err := p.JSON(); err == nil || len(b) != 0 {
		t.Fatal("invalid input returned usable plan bytes")
	}
}

func TestStrictDocumentBoundary(t *testing.T) {
	valid := fixture(t)
	cases := []struct{ name, input, path string }{
		{"duplicate root", strings.Replace(valid, `"source": "file"`, `"source": "DO-NOT-PRINT", "source": "file"`, 1), "$"},
		{"duplicate nested", strings.Replace(valid, `"mode": "dhcp"`, `"mode": "DO-NOT-PRINT", "mode": "dhcp"`, 1), "$.host.network"},
		{"unknown root", strings.Replace(valid, `"source": "file"`, `"source": "file", "DO-NOT-PRINT": "DO-NOT-PRINT"`, 1), "$"},
		{"unknown nested", strings.Replace(valid, `"mode": "dhcp"`, `"mode": "dhcp", "password": "DO-NOT-PRINT"`, 1), "$.host.network"},
		{"trailing document", valid + ` {"password":"DO-NOT-PRINT"}`, "$"},
		{"trailing junk", valid + ` DO-NOT-PRINT`, "$"},
		{"null host", strings.Replace(valid, `"network": { "mode": "dhcp" }`, `"network": null`, 1), "$.host.network"},
		{"wrong string type", strings.Replace(valid, `"source": "file"`, `"source": 7`, 1), "$.source"},
		{"wrong object type", strings.Replace(valid, `"network": { "mode": "dhcp" }`, `"network": []`, 1), "$.host.network"},
		{"wrong array type", strings.Replace(valid, `["time.example.test"]`, `"DO-NOT-PRINT"`, 1), "$.host.time.servers"},
		{"missing field", strings.Replace(valid, `"source": "file",`, ``, 1), "$.source"},
		{"empty input", "", "$"},
		{"root array", "[]", "$"},
		{"oversize", strings.Repeat(" ", 65537), "$"},
		{"excess nesting", strings.Repeat("[", 64) + strings.Repeat("]", 64), "$"},
		{"invalid UTF8", valid + string([]byte{0xff}), "$"},
		{"array bound", strings.Replace(valid, `["time.example.test"]`, `[`+strings.Repeat(`"time.example.test",`, 16)+`"time.example.test"]`, 1), "$.host.time.servers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { reject(t, tc.input, tc.path) })
	}
}

func TestUnsafeOrUnsupportedAnswers(t *testing.T) {
	valid := fixture(t)
	cases := []struct{ name, from, to, path string }{
		{"version", "appliance-answers/v1", "appliance-answers/v2", "$.schema_version"},
		{"source", `"source": "file"`, `"source": "automatic"`, "$.source"},
		{"ambiguous sources", `"source": "file"`, `"source": "file,nocloud"`, "$.source"},
		{"local host", `"owner": "cloud-init"`, `"owner": "local"`, "$.host.owner"},
		{"unknown owner", `"owner": "cloud-init"`, `"owner": "appliance"`, "$.host.owner"},
		{"hostname shell", `"hostname": "olivares.example.test"`, `"hostname": "$(DO-NOT-PRINT)"`, "$.host.hostname"},
		{"hostname leading dash", `"hostname": "olivares.example.test"`, `"hostname": "-bad.test"`, "$.host.hostname"},
		{"hostname non-ASCII case fold", `"hostname": "olivares.example.test"`, `"hostname": "K.test"`, "$.host.hostname"},
		{"hostname label bound", `"hostname": "olivares.example.test"`, `"hostname": "` + strings.Repeat("a", 64) + `.test"`, "$.host.hostname"},
		{"hostname total bound", `"hostname": "olivares.example.test"`, `"hostname": "` + strings.Repeat("a.", 127) + `a"`, "$.host.hostname"},
		{"static unsupported", `"mode": "dhcp"`, `"mode": "static"`, "$.host.network.mode"},
		{"conflicting dhcp address", `"mode": "dhcp"`, `"mode": "dhcp", "address": "192.0.2.1/24"`, "$.host.network"},
		{"time zone unsupported", `"timezone": "UTC"`, `"timezone": "Europe/Madrid"`, "$.host.time.timezone"},
		{"time empty", `["time.example.test"]`, `[]`, "$.host.time.servers"},
		{"time duplicate", `["time.example.test"]`, `["time.example.test","TIME.EXAMPLE.TEST"]`, "$.host.time.servers"},
		{"time invalid", `["time.example.test"]`, `["https://DO-NOT-PRINT"]`, "$.host.time.servers[0]"},
		{"time non-ASCII case fold", `["time.example.test"]`, `["K.test"]`, "$.host.time.servers[0]"},
		{"time multicast", `["time.example.test"]`, `["224.0.0.1"]`, "$.host.time.servers[0]"},
		{"time too many", `["time.example.test"]`, `[` + strings.Repeat(`"time.example.test",`, 8) + `"last.example.test"]`, "$.host.time.servers"},
		{"SSH private", `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAARIjNEVWZ3iJmqu8zd7v8AESIzRFVmd4iZqrvM3e7/`, "-----BEGIN DO-NOT-PRINT PRIVATE KEY-----", "$.host.ssh_authorized_keys[0]"},
		{"SSH options", `ssh-ed25519 AAAA`, `command=DO-NOT-PRINT ssh-ed25519 AAAA`, "$.host.ssh_authorized_keys[0]"},
		{"SSH invalid wire", `AAAAC3NzaC1lZDI1NTE5AAAAIAARIjNEVWZ3iJmqu8zd7v8AESIzRFVmd4iZqrvM3e7/`, `RE8tTk9ULVBSSU5U`, "$.host.ssh_authorized_keys[0]"},
		{"SSH truncated", `AAAAC3NzaC1lZDI1NTE5AAAAIAARIjNEVWZ3iJmqu8zd7v8AESIzRFVmd4iZqrvM3e7/`, `AAAAC3NzaC1lZDI1NTE5AAAAIAARIjNEVWZ3`, "$.host.ssh_authorized_keys[0]"},
		{"unsupported profile", "single-node-prod", "postgres-prod", "$.product.storage_profile"},
		{"literal DSN", `"storage_profile": "single-node-prod"`, `"storage_profile": "single-node-prod", "dsn": "postgres://user:DO-NOT-PRINT@db"`, "$.product"},
		{"inference role", `"node_role": "control"`, `"node_role": "inference"`, "$.product.node_role"},
		{"unknown channel", `"update_channel": "stable"`, `"update_channel": "nightly"`, "$.product.update_channel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { reject(t, strings.Replace(valid, tc.from, tc.to, 1), tc.path) })
	}
	for _, url := range []string{"http://example.test", "https://user:DO-NOT-PRINT@example.test", "https://example.test/?token=DO-NOT-PRINT", "https://example.test/#DO-NOT-PRINT", "https://example.test/DO-NOT-PRINT", "https://", "https://example.test:0", "https://example.test:65536", "https://example.test:", "https://127.0.0.1", "https://[::]", "https://224.0.0.1", "https://a..test", "https://localhost", "https://%65xample.test", " https://example.test"} {
		t.Run("URL "+strings.ReplaceAll(url, "DO-NOT-PRINT", "redacted"), func(t *testing.T) {
			reject(t, strings.Replace(valid, "https://olivares.example.test", url, 1), "$.product.public_console_url")
		})
	}
}

func TestCanonicalEquivalentAnswers(t *testing.T) {
	valid := fixture(t)
	first, err := answers.Build(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	want, err := first.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var unordered map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(valid, "olivares.example.test", "OLIVARES.EXAMPLE.TEST")), &unordered); err != nil {
		t.Fatal(err)
	}
	reordered, err := json.Marshal(unordered)
	if err != nil {
		t.Fatal(err)
	}
	second, err := answers.Build(strings.NewReader(string(reordered)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := second.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("equivalent documents changed canonical plan:\n%s", got)
	}
	if len(got) == 0 || got[len(got)-1] != '\n' {
		t.Fatal("canonical output needs terminal newline")
	}
}

type brokenInput struct{}

func (brokenInput) Read([]byte) (int, error) { return 0, errors.New("DO-NOT-PRINT") }

func TestReadFailureIsNotValidationOrSecretDisclosure(t *testing.T) {
	p, err := answers.Build(brokenInput{})
	if !errors.Is(err, answers.ErrCannotRead) || strings.Contains(err.Error(), "DO-NOT-PRINT") {
		t.Fatalf("wrong transport error: %v", err)
	}
	if b, err := p.JSON(); err == nil || len(b) > 0 {
		t.Fatal("read failure returned a usable plan")
	}
}

func TestSupportedProvenanceChannelsAndIPv6(t *testing.T) {
	valid := fixture(t)
	for _, source := range []string{"file", "nocloud", "guestinfo", "systemd-credential", "local-assistant"} {
		for _, channel := range []string{"stable", "security", "lts"} {
			input := strings.Replace(valid, `"source": "file"`, `"source": "`+source+`"`, 1)
			input = strings.Replace(input, `"update_channel": "stable"`, `"update_channel": "`+channel+`"`, 1)
			input = strings.Replace(input, "https://olivares.example.test", "https://[2001:db8::1]:443/", 1)
			input = strings.Replace(input, `["time.example.test"]`, `["2001:db8::2","time.example.test"]`, 1)
			p, err := answers.Build(strings.NewReader(input))
			if err != nil {
				t.Fatalf("supported %s/%s: %v", source, channel, err)
			}
			b, err := p.JSON()
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`"public_console_url": "https://[2001:db8::1]"`, `"source": "` + source + `"`, `"update_channel": "` + channel + `"`, `"storage_profile": "single-node-prod"`, `"node_role": "control"`} {
				if !strings.Contains(string(b), want) {
					t.Fatalf("lost supported setting %s", want)
				}
			}
		}
	}
}

func TestKernelHostnameBoundary(t *testing.T) {
	valid := fixture(t)
	for _, name := range []string{
		"10.0.0.1", "1234", "host.1234", "2001:db8::1",
		strings.Repeat("a", 64),        // A single label still has a 63-byte bound.
		strings.Repeat("a", 63) + ".b", // 65 bytes, individually valid labels.
		strings.Repeat(strings.Repeat("a", 60)+".", 3) + strings.Repeat("b", 60), // 243 bytes.
	} {
		t.Run("refuse "+name, func(t *testing.T) {
			input := strings.Replace(valid, `"hostname": "olivares.example.test"`, `"hostname": "`+name+`"`, 1)
			reject(t, input, "$.host.hostname")
		})
	}
	for _, name := range []string{"SERVER", "1234.example", "node-12.example.test", strings.Repeat("a", 63), strings.Repeat("A", 62) + ".B"} {
		t.Run("retain "+name, func(t *testing.T) {
			input := strings.Replace(valid, `"hostname": "olivares.example.test"`, `"hostname": "`+name+`"`, 1)
			plan, err := answers.Build(strings.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			data, err := plan.JSON()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"hostname": "`+strings.ToLower(name)+`"`) {
				t.Fatalf("complete hostname was truncated or altered: %s", data)
			}
		})
	}
}

func TestTimeEndpointZoneBeforeNormalization(t *testing.T) {
	valid := fixture(t)
	for _, endpoint := range []string{"::ffff:192.0.2.1%eth0", "2001:db8::1%eth0", "fe80::1%eth0"} {
		t.Run("refuse "+endpoint, func(t *testing.T) {
			input := strings.Replace(valid, `"time.example.test"`, `"`+endpoint+`"`, 1)
			reject(t, input, "$.host.time.servers[0]")
		})
	}
	for _, tc := range []struct{ input, normalized string }{
		{"::ffff:192.0.2.1", "192.0.2.1"}, {"2001:db8::1", "2001:db8::1"}, {"TIME.EXAMPLE.TEST", "time.example.test"},
	} {
		t.Run("retain "+tc.input, func(t *testing.T) {
			input := strings.Replace(valid, `"time.example.test"`, `"`+tc.input+`"`, 1)
			plan, err := answers.Build(strings.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			data, err := plan.JSON()
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Answers struct {
					Host struct{ Time struct{ Servers []string } }
				}
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Answers.Host.Time.Servers) != 1 || got.Answers.Host.Time.Servers[0] != tc.normalized {
				t.Fatalf("incorrect endpoint normalization: %s", data)
			}
		})
	}
}
