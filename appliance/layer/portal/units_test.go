// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// unit maps a section name to its directives; a directive may repeat.
type unit map[string]map[string][]string

func (u unit) values(section, key string) []string { return u[section][key] }

// readUnit parses the systemd unit file at path.
func readUnit(t *testing.T, path string) unit {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return parseUnit(t, path, string(data))
}

// parseUnit parses unit text: sections, KEY=VALUE directives, comments and backslash
// continuations.
func parseUnit(t *testing.T, name, text string) unit {
	t.Helper()
	u := unit{}
	section := ""
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		for strings.HasSuffix(line, `\`) && i+1 < len(lines) {
			i++
			line = strings.TrimSuffix(line, `\`) + " " + strings.TrimSpace(lines[i])
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			section = line[1 : len(line)-1]
			if u[section] == nil {
				u[section] = map[string][]string{}
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			t.Fatalf("%s:%d: not a directive: %q", name, i+1, line)
		}
		key = strings.TrimSpace(key)
		u[section][key] = append(u[section][key], strings.TrimSpace(value))
	}
	return u
}

// serviceHardening lists every value the service assigns to each directive, in order.
// Requires= is in [Unit]; every other directive is in [Service].
var serviceHardening = map[string][]string{
	"DynamicUser":             {"yes"},
	"NoNewPrivileges":         {"true"},
	"ProtectSystem":           {"strict"},
	"ProtectHome":             {"true"},
	"PrivateTmp":              {"true"},
	"PrivateDevices":          {"true"},
	"PrivateNetwork":          {"yes"},
	"RestrictAddressFamilies": {"AF_UNIX AF_INET AF_INET6"},
	"IPAddressDeny":           {"any"},
	"IPAddressAllow":          {"localhost"},
	"ProtectClock":            {"true"},
	"ProtectHostname":         {"true"},
	"ProtectKernelTunables":   {"true"},
	"ProtectKernelModules":    {"true"},
	"ProtectKernelLogs":       {"true"},
	"ProtectControlGroups":    {"true"},
	"RestrictNamespaces":      {"true"},
	"RestrictSUIDSGID":        {"true"},
	"RestrictRealtime":        {"true"},
	"LockPersonality":         {"true"},
	"MemoryDenyWriteExecute":  {"true"},
	"SystemCallArchitectures": {"native"},
	"SystemCallFilter":        {"@system-service", "~@privileged @resources"},
	"CapabilityBoundingSet":   {""},
	"AmbientCapabilities":     {""},
	"UMask":                   {"0027"},
	"Environment":             {"OLIVARES_PORTAL_TLS_DIRECTORY=/etc/olivares-portal"},
	"LoadCredential":          {"tls.crt:/etc/olivares-portal/tls.crt", "tls.key:/etc/olivares-portal/tls.key"},
	"Requires":                {"olivares-portal.socket"},
}

// serviceOtherKeys are the [Service] directives the service assigns besides
// serviceHardening; their values are asserted where the test reads them.
var serviceOtherKeys = []string{"ExecStart", "Type", "User"}

// hardeningViolations names, sorted, each directive of serviceHardening that u does not
// assign exactly as listed, and each [Service] directive that is neither listed nor in
// serviceOtherKeys. A missing, reordered or additional assignment is a violation: the
// service manager applies a later assignment over an earlier one, and an empty one resets
// a list. An unlisted directive is one too, because adding one can weaken the service as
// surely as changing one.
func hardeningViolations(u unit) []string {
	var violations []string
	for key, want := range serviceHardening {
		section := "Service"
		if key == "Requires" {
			section = "Unit"
		}
		if !slices.Equal(u.values(section, key), want) {
			violations = append(violations, key)
		}
	}
	for key := range u["Service"] {
		if _, listed := serviceHardening[key]; (!listed || key == "Requires") && !slices.Contains(serviceOtherKeys, key) {
			violations = append(violations, key)
		}
	}
	slices.Sort(violations)
	return violations
}

func TestPortalUnits_SocketAndServiceAreShippedDisabledAndHardened(t *testing.T) {
	socket := readUnit(t, "units/olivares-portal.socket")
	service := readUnit(t, "units/olivares-portal.service")

	t.Run("shipped disabled", func(t *testing.T) {
		for name, u := range map[string]unit{"socket": socket, "service": service} {
			if _, ok := u["Install"]; ok {
				t.Errorf("the %s unit has an [Install] section, so packaging would enable it at boot", name)
			}
			for section := range u {
				for _, key := range []string{"WantedBy", "RequiredBy", "UpheldBy", "Alias", "Also"} {
					if len(u.values(section, key)) > 0 {
						t.Errorf("the %s unit declares %s=", name, key)
					}
				}
			}
		}
	})

	t.Run("the socket listens on loopback only", func(t *testing.T) {
		streams := socket.values("Socket", "ListenStream")
		if len(streams) == 0 {
			t.Fatal("the socket declares no ListenStream=")
		}
		for _, stream := range streams {
			address, err := netip.ParseAddrPort(stream)
			if err != nil || !address.Addr().IsLoopback() || address.Port() != 9443 {
				t.Errorf("ListenStream=%s is not a loopback address on port 9443", stream)
			}
		}
		for _, key := range []string{"ListenDatagram", "ListenSequentialPacket", "ListenFIFO", "ListenSpecial", "ListenNetlink", "ListenMessageQueue", "ListenUSBFunction", "BindToDevice", "FreeBind", "Accept"} {
			if got := socket.values("Socket", key); len(got) > 0 {
				t.Errorf("the socket declares %s=%v", key, got)
			}
		}
		for key, want := range map[string]string{"IPAddressDeny": "any", "IPAddressAllow": "localhost"} {
			if !slices.Equal(socket.values("Socket", key), []string{want}) {
				t.Errorf("socket %s=%v, want %s", key, socket.values("Socket", key), want)
			}
		}
	})

	t.Run("the service is hardened and unprivileged", func(t *testing.T) {
		if violations := hardeningViolations(service); len(violations) > 0 {
			t.Errorf("directives not assigned exactly as listed: %v", violations)
		}
		// The console measures the directory Environment= names; the service manager
		// delivers the files LoadCredential= names. They must be the same two files.
		var source string
		for _, value := range service.values("Service", "Environment") {
			if dir, ok := strings.CutPrefix(value, "OLIVARES_PORTAL_TLS_DIRECTORY="); ok {
				source = dir
			}
		}
		delivered := []string{
			CertificateFile + ":" + filepath.Join(source, CertificateFile),
			KeyFile + ":" + filepath.Join(source, KeyFile),
		}
		if source == "" || !slices.Equal(service.values("Service", "LoadCredential"), delivered) {
			t.Errorf("LoadCredential=%v does not deliver the files of the measured directory %q",
				service.values("Service", "LoadCredential"), source)
		}
		for _, key := range []string{"User", "Group"} {
			for _, value := range service.values("Service", key) {
				if value == "" || value == "root" || value == "0" {
					t.Errorf("service %s=%q", key, value)
				}
			}
		}
		if len(service.values("Service", "User")) != 1 {
			t.Error("the service names exactly one User=")
		}
		for key, values := range service["Service"] {
			if !strings.HasPrefix(key, "Exec") {
				continue
			}
			for _, value := range values {
				if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "!") {
					t.Errorf("%s=%s runs with elevated privileges", key, value)
				}
			}
		}
		start := service.values("Service", "ExecStart")
		if len(start) != 1 || len(strings.Fields(start[0])) != 1 {
			t.Errorf("ExecStart=%v must be the binary alone, with no listen argument", start)
		}
		if got := service.values("Service", "PermissionsStartOnly"); len(got) > 0 {
			t.Errorf("PermissionsStartOnly=%v", got)
		}
	})
}

func TestPortalUnits_ALaterAssignmentCannotWeakenAHardeningDirective(t *testing.T) {
	shipped, err := os.ReadFile("units/olivares-portal.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ appended, key string }{
		{"PrivateNetwork=no", "PrivateNetwork"},
		{"NoNewPrivileges=false", "NoNewPrivileges"},
		{"ProtectSystem=false", "ProtectSystem"},
		{"SystemCallFilter=", "SystemCallFilter"},
		{"IPAddressAllow=any", "IPAddressAllow"},
		{"CapabilityBoundingSet=CAP_NET_ADMIN", "CapabilityBoundingSet"},
		{"RestrictAddressFamilies=AF_PACKET", "RestrictAddressFamilies"},
		{"LoadCredential=extra:/etc/olivares-portal/extra", "LoadCredential"},
		{"Environment=OLIVARES_PORTAL_TLS_DIRECTORY=/var/tmp", "Environment"},
		{"ReadWritePaths=/etc", "ReadWritePaths"},
		{"BindPaths=/etc", "BindPaths"},
		{"SupplementaryGroups=adm", "SupplementaryGroups"},
		{"PrivateUsers=no", "PrivateUsers"},
	} {
		t.Run(tc.appended, func(t *testing.T) {
			// The shipped unit ends in [Service], so the appended line lands there and,
			// being later, is what the service manager applies.
			u := parseUnit(t, "appended.service", string(shipped)+tc.appended+"\n")
			if !slices.Contains(hardeningViolations(u), tc.key) {
				t.Fatalf("appending %q leaves %s= unflagged", tc.appended, tc.key)
			}
		})
	}
}
