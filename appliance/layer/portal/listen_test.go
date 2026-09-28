// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTLSPair returns a self-signed certificate, its private key and the certificate's
// SHA-256 fingerprint in hex.
func newTLSPair(t *testing.T) (certPEM, keyPEM []byte, fingerprint string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "appliance.example.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"appliance.example.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		hex.EncodeToString(sum[:])
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile applies the umask; each case needs its exact mode.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// writeTLSPair writes a certificate and its key into dir, the key with keyMode, and
// returns the certificate fingerprint.
func writeTLSPair(t *testing.T, dir string, keyMode os.FileMode) string {
	t.Helper()
	certPEM, keyPEM, fingerprint := newTLSPair(t)
	writeFile(t, filepath.Join(dir, CertificateFile), certPEM, 0o644)
	writeFile(t, filepath.Join(dir, KeyFile), keyPEM, keyMode)
	return fingerprint
}

func verifiedCustody(t *testing.T) Custody {
	t.Helper()
	dir := t.TempDir()
	fingerprint := writeTLSPair(t, dir, 0o600)
	custody, cert := CheckTLSCustody(dir, dir)
	if !custody.Verified || cert == nil || custody.CertificateSHA256 != fingerprint {
		t.Fatalf("a private, matching pair must verify: %+v", custody)
	}
	return custody
}

func enabled(v bool) *bool { return &v }

// remoteSelection asks for remote exposure on one management interface.
func remoteSelection() Selection {
	return Selection{Enabled: enabled(true), Listen: ListenManagement, ManagementInterfaces: []string{"eth0"}}
}

func measuredFirewall() FirewallMeasurement {
	return FirewallMeasurement{Holds: true, Policy: Measured("management policy applied")}
}

var (
	loopbackV4   = &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9443}
	loopbackV6   = &net.TCPAddr{IP: net.ParseIP("::1"), Port: 9443}
	anyV4        = &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 9443}
	anyV6        = &net.TCPAddr{IP: net.ParseIP("::"), Port: 9443}
	managementV4 = &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 9443}
	managementV6 = &net.TCPAddr{IP: net.ParseIP("2001:db8::10"), Port: 9443}
)

// onDevice is the address of a passed socket together with the network interface the
// kernel binds that socket to; "" when it is bound to none.
type onDevice struct {
	*net.TCPAddr
	device string
}

func (a onDevice) BoundDevice() string { return a.device }

// bound returns the address ip:9443 of a socket bound to device.
func bound(ip, device string) onDevice {
	return onDevice{TCPAddr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 9443}, device: device}
}

// assertNotRemote fails when d enables remote exposure or would serve any non-loopback
// or wildcard address.
func assertNotRemote(t *testing.T, d Decision) {
	t.Helper()
	if d.Mode == Remote {
		t.Fatalf("remote listener enabled: %+v", d)
	}
	for _, addr := range []net.Addr{anyV4, anyV6, managementV4, managementV6, bound("192.0.2.10", "eth0")} {
		if d.Serves(addr) {
			t.Fatalf("%s decision serves %s", d.Mode, addr)
		}
	}
}

func TestPortalListen_RefusesWhenFirewallPrerequisiteUnmeasured(t *testing.T) {
	custody := verifiedCustody(t)

	d := Decide(remoteSelection(), custody, NoFirewallProbe{}.MeasureFirewall())
	if d.Mode != LocalOnly || !slices.Equal(d.Reasons, []string{"firewall prerequisite is unmeasured"}) {
		t.Fatalf("the default firewall probe must keep the listener local: %+v", d)
	}
	assertNotRemote(t, d)
	if !d.Serves(loopbackV4) {
		t.Fatal("the local status must stay available")
	}

	// A probe that ran and found the prerequisite not held refuses, and says so.
	failed := FirewallMeasurement{Policy: Measured("input policy accept")}
	d = Decide(remoteSelection(), custody, failed)
	if !slices.Equal(d.Reasons, []string{"firewall prerequisite is not held"}) {
		t.Fatalf("a measured firewall that does not hold is not unmeasured: %+v", d)
	}
	assertNotRemote(t, d)

	// The snapshot the service serves uses the probe it is given; the default refuses.
	s := Snapshot(remoteSelection(), custody, NoFirewallProbe{})
	if s.Listen.Mode != LocalOnly || s.Firewall.Holds {
		t.Fatalf("the default probe was not consulted: %+v", s.Listen)
	}

	// Control: the same inputs with a measured prerequisite enable the packaged capability.
	if d := Decide(remoteSelection(), custody, measuredFirewall()); d.Mode != Remote || !d.Serves(bound("192.0.2.10", "eth0")) {
		t.Fatalf("a measured prerequisite should enable remote exposure: %+v", d)
	}
}

func TestPortalListen_RemoteIsDisabledUntilAnInterfaceIsSelectedAndTLSCustodyIsVerified(t *testing.T) {
	custody := verifiedCustody(t)

	t.Run("packaged capability with every prerequisite held", func(t *testing.T) {
		d := Decide(remoteSelection(), custody, measuredFirewall())
		if d.Mode != Remote || !slices.Equal(d.Interfaces, []string{"eth0"}) || len(d.Reasons) != 0 {
			t.Fatalf("every prerequisite held: %+v", d)
		}
		if !d.Serves(bound("192.0.2.10", "eth0")) || !d.Serves(bound("2001:db8::10", "eth0")) || !d.Serves(loopbackV4) {
			t.Fatalf("remote exposure should serve sockets bound to eth0: %+v", d)
		}
		if d.Serves(anyV4) || d.Serves(anyV6) {
			t.Fatal("a wildcard address is never served, even with remote exposure enabled")
		}
	})

	for _, tc := range []struct {
		name   string
		change func(*Selection)
		reason string
	}{
		{"no management interface selected", func(s *Selection) { s.ManagementInterfaces = nil }, "no management interface is selected"},
		{"listen scope local", func(s *Selection) { s.Listen = ListenLocal }, "portal.listen is not management"},
		{"listen scope absent", func(s *Selection) { s.Listen = "" }, "portal.listen is not management"},
		{"listen scope wildcard", func(s *Selection) { s.Listen = "0.0.0.0" }, "portal.listen is not management"},
		{"enabled absent", func(s *Selection) { s.Enabled = nil }, "portal.enabled is not true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := remoteSelection()
			tc.change(&selection)
			d := Decide(selection, custody, measuredFirewall())
			if d.Mode != LocalOnly || !slices.Equal(d.Reasons, []string{tc.reason}) {
				t.Fatalf("want local only because %q: %+v", tc.reason, d)
			}
			assertNotRemote(t, d)
			if !d.Serves(loopbackV4) || !d.Serves(loopbackV6) {
				t.Fatal("local only still serves loopback")
			}
		})
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"no TLS material", func(*testing.T, string) {}},
		{"key readable by its group", func(t *testing.T, dir string) { writeTLSPair(t, dir, 0o640) }},
		{"key readable by others", func(t *testing.T, dir string) { writeTLSPair(t, dir, 0o604) }},
		{"certificate without key", func(t *testing.T, dir string) {
			writeTLSPair(t, dir, 0o600)
			if err := os.Remove(filepath.Join(dir, KeyFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{"key is a symbolic link", func(t *testing.T, dir string) {
			elsewhere := t.TempDir()
			writeTLSPair(t, elsewhere, 0o600)
			certPEM, err := os.ReadFile(filepath.Join(elsewhere, CertificateFile))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, CertificateFile), certPEM, 0o644)
			if err := os.Symlink(filepath.Join(elsewhere, KeyFile), filepath.Join(dir, KeyFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{"key does not match the certificate", func(t *testing.T, dir string) {
			writeTLSPair(t, dir, 0o600)
			_, otherKey, _ := newTLSPair(t)
			writeFile(t, filepath.Join(dir, KeyFile), otherKey, 0o600)
		}},
		{"certificate is not PEM", func(t *testing.T, dir string) {
			writeTLSPair(t, dir, 0o600)
			writeFile(t, filepath.Join(dir, CertificateFile), []byte("not a certificate\n"), 0o644)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			unverified, cert := CheckTLSCustody(dir, dir)
			if unverified.Verified || cert != nil || unverified.CertificateSHA256 != "" {
				t.Fatalf("custody verified material outside the rule: %+v", unverified)
			}
			if unverified.Reason == "" || strings.Contains(unverified.Reason, dir) || strings.Contains(unverified.Reason, "PRIVATE KEY") {
				t.Fatalf("custody reason must be a fixed phrase without paths or key material: %q", unverified.Reason)
			}
			d := Decide(remoteSelection(), unverified, measuredFirewall())
			if d.Mode != Disabled || len(d.Reasons) != 1 || !strings.HasPrefix(d.Reasons[0], "TLS custody is unverified") {
				t.Fatalf("no verified TLS material must disable the listener: %+v", d)
			}
			assertNotRemote(t, d)
			if d.Serves(loopbackV4) {
				t.Fatal("a disabled console serves nothing, not even loopback")
			}
		})
	}

	t.Run("no directories", func(t *testing.T) {
		unverified, cert := CheckTLSCustody("", "")
		if unverified.Verified || cert != nil {
			t.Fatalf("custody verified without directories: %+v", unverified)
		}
		assertNotRemote(t, Decide(remoteSelection(), unverified, measuredFirewall()))
	})

	t.Run("portal explicitly disabled", func(t *testing.T) {
		selection := remoteSelection()
		selection.Enabled = enabled(false)
		d := Decide(selection, custody, measuredFirewall())
		if d.Mode != Disabled || !slices.Equal(d.Reasons, []string{"portal.enabled is false"}) || d.Serves(loopbackV4) {
			t.Fatalf("portal.enabled false must disable the console: %+v", d)
		}
	})

	t.Run("nothing measured", func(t *testing.T) {
		d := Decide(Selection{}, Custody{}, NoFirewallProbe{}.MeasureFirewall())
		if d.Mode != Disabled {
			t.Fatalf("zero inputs must disable the console: %+v", d)
		}
		assertNotRemote(t, d)
	})

	t.Run("the decision does not alias its input", func(t *testing.T) {
		selection := remoteSelection()
		d := Decide(selection, custody, measuredFirewall())
		selection.ManagementInterfaces[0] = "eth9"
		if d.Interfaces[0] != "eth0" {
			t.Fatal("the decision changed after its input changed")
		}
	})

	t.Run("the service never binds a socket itself", func(t *testing.T) {
		for _, site := range bindSites(nonTestSources(t)) {
			t.Errorf("binds a socket itself: %s", site)
		}
	})
}

func TestPortalListen_RemoteServesOnlySocketsBoundToASelectedInterface(t *testing.T) {
	custody := verifiedCustody(t)
	remote := Decide(remoteSelection(), custody, measuredFirewall())
	if remote.Mode != Remote {
		t.Fatalf("every prerequisite held: %+v", remote)
	}
	for _, tc := range []struct {
		name  string
		addr  net.Addr
		serve bool
	}{
		{"selected interface, IPv4", bound("192.0.2.10", "eth0"), true},
		{"selected interface, IPv6", bound("2001:db8::10", "eth0"), true},
		{"loopback bound to no interface", bound("127.0.0.1", ""), true},
		{"loopback, binding unmeasured", loopbackV4, true},
		{"unselected uplink", bound("203.0.113.7", "eth1"), false},
		{"unselected uplink, binding unmeasured", &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 9443}, false},
		{"selected address, binding unmeasured", managementV4, false},
		{"selected address bound to no interface", bound("192.0.2.10", ""), false},
		{"selected address bound to the uplink", bound("192.0.2.10", "eth1"), false},
		{"IPv4 wildcard bound to the selected interface", bound("0.0.0.0", "eth0"), false},
		{"IPv6 wildcard bound to the selected interface", bound("::", "eth0"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := remote.Serves(tc.addr); got != tc.serve {
				t.Fatalf("Serves(%s) = %t, want %t", tc.addr, got, tc.serve)
			}
		})
	}

	t.Run("two selected interfaces", func(t *testing.T) {
		selection := remoteSelection()
		selection.ManagementInterfaces = []string{"eth1", "eth0"}
		d := Decide(selection, custody, measuredFirewall())
		if !d.Serves(bound("203.0.113.7", "eth1")) || d.Serves(bound("198.51.100.4", "wlan0")) {
			t.Fatalf("remote access serves exactly the selected interfaces: %+v", d)
		}
	})

	t.Run("a refusal names the interface and nothing else", func(t *testing.T) {
		for addr, want := range map[net.Addr]string{
			bound("203.0.113.7", "eth1"): `the socket is bound to "eth1", which is not a selected management interface`,
			managementV4:                 "the socket is bound to no management interface",
			bound("0.0.0.0", "eth0"):     "a wildcard address is never served",
			bound("192.0.2.10", "eth0"):  "",
		} {
			if got := remote.Refusal(addr); got != want {
				t.Errorf("Refusal(%s) = %q, want %q", addr, got, want)
			}
		}
	})

	t.Run("local only serves no interface's socket", func(t *testing.T) {
		selection := remoteSelection()
		selection.Listen = ListenLocal
		d := Decide(selection, custody, measuredFirewall())
		if d.Mode != LocalOnly || d.Serves(bound("192.0.2.10", "eth0")) {
			t.Fatalf("local only served a management interface: %+v", d)
		}
	})
}

// bindingIdentifiers lists, per import path, the identifiers that create, bind or listen
// on a socket. The net.Listener type and net.FileListener, which adopts a passed
// descriptor, bind nothing.
var bindingIdentifiers = map[string][]string{
	"net":                   {"Listen", "ListenTCP", "ListenUDP", "ListenIP", "ListenUnix", "ListenUnixgram", "ListenPacket", "ListenMulticastUDP", "ListenConfig"},
	"crypto/tls":            {"Listen"},
	"net/http":              {"ListenAndServe", "ListenAndServeTLS"},
	"syscall":               {"Socket", "Bind", "Listen", "SYS_SOCKET", "SYS_BIND", "SYS_LISTEN"},
	"golang.org/x/sys/unix": {"Socket", "Bind", "Listen", "SYS_SOCKET", "SYS_BIND", "SYS_LISTEN"},
}

// bindSites returns each place in files that binds a socket: a binding identifier reached
// through the name the file gives its import, a dot import of one of those packages, or
// any ListenAndServe method.
func bindSites(files []sourceFile) []string {
	var sites []string
	for _, file := range files {
		imported := map[string]string{} // the file's name for a watched import → its path
		for _, spec := range file.syntax.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if _, watched := bindingIdentifiers[path]; err != nil || !watched {
				continue
			}
			name := path[strings.LastIndex(path, "/")+1:]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			if name == "." {
				sites = append(sites, file.path+": dot import of "+path)
				continue
			}
			imported[name] = path
		}
		ast.Inspect(file.syntax, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if strings.HasPrefix(sel.Sel.Name, "ListenAndServe") {
				sites = append(sites, file.path+": "+sel.Sel.Name)
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok {
				if path, ok := imported[pkg.Name]; ok && slices.Contains(bindingIdentifiers[path], sel.Sel.Name) {
					sites = append(sites, file.path+": "+path+"."+sel.Sel.Name)
				}
			}
			return true
		})
	}
	return sites
}

func TestPortalListen_SelfBindScanResolvesImportsAndSystemCalls(t *testing.T) {
	for _, tc := range []struct {
		name  string
		src   string
		binds bool
	}{
		{"net.Listen", `import "net"; func f() { net.Listen("tcp", ":1") }`, true},
		{"aliased net", `import n "net"; func f() { n.Listen("tcp", ":1") }`, true},
		{"dot-imported net", `import . "net"; func f() { Listen("tcp", ":1") }`, true},
		{"net.ListenConfig", `import "net"; var lc net.ListenConfig`, true},
		{"aliased tls", `import t "crypto/tls"; func f() { t.Listen("tcp", ":1", nil) }`, true},
		{"http server method", `import "net/http"; func f(s *http.Server) { s.ListenAndServe() }`, true},
		{"syscall.Socket", `import "syscall"; func f() { syscall.Socket(2, 1, 0) }`, true},
		{"syscall.Bind", `import "syscall"; func f() { syscall.Bind(3, nil) }`, true},
		{"syscall.Listen", `import "syscall"; func f() { syscall.Listen(3, 1) }`, true},
		{"raw bind system call", `import "syscall"; func f() { syscall.Syscall(syscall.SYS_BIND, 3, 0, 0) }`, true},
		{"x/sys/unix.Bind", `import "golang.org/x/sys/unix"; func f() { unix.Bind(3, nil) }`, true},
		{"aliased x/sys/unix.Listen", `import u "golang.org/x/sys/unix"; func f() { u.Listen(3, 1) }`, true},
		{"the Listener type", `import "net"; var l net.Listener`, false},
		{"a passed descriptor", `import ("net"; "os"); func f(fd *os.File) { net.FileListener(fd) }`, false},
		{"a socket option read", `import "syscall"; func f() { syscall.Syscall6(syscall.SYS_GETSOCKOPT, 3, 1, 25, 0, 0, 0) }`, false},
		{"a field named Listen", `type s struct{ Listen string }; func f(v s) string { return v.Listen }`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			syntax, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture; "+tc.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			sites := bindSites([]sourceFile{{path: "fixture.go", syntax: syntax}})
			if got := len(sites) > 0; got != tc.binds {
				t.Fatalf("bind sites %v, want binding %t", sites, tc.binds)
			}
		})
	}
}
