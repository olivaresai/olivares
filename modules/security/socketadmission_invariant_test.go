// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestInvariant_EverySocketGoesThroughTheAdmissionPoint is the CLASS guard for
//. It is the deliverable that matters most, because the defect it exists to
// stop is not a bug — it is a shape.
//
// The history: the rule "a listener may only be reachable off-host if its traffic
// is protected or the operator declared it" had been written SEVEN separate times
// in this tree — once in the engine's serve path and once inside each of
// connectors/{aaa,claude,claude-managed-agents,cowork,envoy,ssf} — and THREE more
// listeners had no copy at all (connectors/{github,gitlab,tak}, found by the
// external the model contrast of PR #565, finding H-03). The seven copies had
// already drifted: claude-managed-agents classified with strings.EqualFold, which
// folds UNICODE, so it answered "loopback" for the host "localhoſt" (U+017F) —
// a spelling that is not the name it checked for.
//
// Patching the three would have fixed the three. This test is what stops the
// FOURTH: it fails when any component opens a listening socket without going
// through sdk/netbind, naming the file, the line and the call.
//
// It is deliberately blunt. It does not try to decide whether a given direct
// bind is "actually safe" — that judgement is what produced seven divergent
// copies. Outside the admission point there are only two states: through
// netbind, or on the named exception list below with a reason.
//
// ITS HONEST BOUND, stated because the looser claim is tempting and false. This
// proves that no file in the scanned trees CALLS a listener constructor outside
// netbind. That is not the same as "every socket in the product goes through
// netbind", and the difference is not academic: a listener opened INSIDE a
// dependency has no such call to find. The external the model contrast of this
// branch demonstrated exactly that — operator/ binds :8080 and :8081 through
// controller-runtime's manager, and this scan walked straight past both until
// that constructor was added to the detected set below. Other wrapper-mediated
// listeners (go-plugin, the Terraform provider's plugin serve path) are reachable
// the same way. Every such library that binds must be named in `qualified`, and
// one that is not is a hole this test cannot see. Adding a dependency that
// listens is therefore a change that has to be brought here by hand.
func TestInvariant_EverySocketGoesThroughTheAdmissionPoint(t *testing.T) {
	root := repoRoot(t)

	found := scanForDirectSocketCalls(t, root, scanDirs)

	// Every finding must be covered by an exception, or the invariant is broken.
	var violations []string
	hit := map[string]int{}
	for _, f := range found {
		if reason, ok := allowedDirectBinds[f.file]; ok {
			hit[f.file]++
			_ = reason
			continue
		}
		violations = append(violations, f.String())
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("socket-admission: %s\n"+
			"    This component opens a listening socket without the product's single admission point.\n"+
			"    Use sdk/netbind (Listen / ListenPacket / ListenMulticastUDP), or — if it genuinely\n"+
			"    cannot — add it to allowedDirectBinds in this file WITH A REASON, so the exposure is\n"+
			"    a decision somebody made and not one nobody noticed.", v)
	}

	// An exception that no longer applies is worse than no exception: it is a
	// standing permission nobody is using and nobody will re-examine. If a file on
	// the list stopped binding directly, the entry must go.
	for file := range allowedDirectBinds {
		if hit[file] != 0 {
			continue
		}
		// "The scan found no direct bind here" has TWO causes and they are not the
		// same defect. Either the file stopped binding — a stale exception, which
		// must go — or the subtree it lives in is not part of the tree under test.
		// The published export carries neither cloud/ nor commercial/, so reading
		// their absence as a stale exception fails a tree that is behaving exactly
		// as designed. Absence of the whole subtree is checked, not absence of the
		// file: a typo inside a subtree that IS here still has to fail.
		top := file
		if i := strings.Index(top, "/"); i >= 0 {
			top = top[:i]
		}
		if info, err := os.Stat(filepath.Join(root, top)); err != nil || !info.IsDir() {
			t.Logf("socket-admission: %s is not in this tree (%s/ is absent); its exception does not apply here, which is not the same as being stale", file, top)
			continue
		}
		t.Errorf("socket-admission: %s is on allowedDirectBinds but opens no direct socket any more — remove the exception; a permission that is not needed must not be left lying around", file)
	}

	// A sweep that finds nothing has not proved the tree is clean; it has proved
	// nothing about its own shape. The admission point itself contains the only
	// legitimate direct binds in the repository, so if the scanner cannot see
	// THOSE, it is broken and every "clean" verdict above is worthless.
	self := scanForDirectSocketCalls(t, root, []string{filepath.Join("sdk", "netbind")})
	if len(self) < 3 {
		t.Fatalf("socket-admission: the scanner found %d direct binds inside sdk/netbind itself; it must see at least the three constructors (Listen, ListenPacket, ListenMulticastUDP). The scan is broken, so its verdict on everything else means nothing", len(self))
	}
}

// scanDirs are the source trees this invariant covers. cloud/ and commercial/ are
// separate Go modules outside go.work and are scanned too: being out of the
// workspace is a build-graph fact, not an exemption from the policy.
var scanDirs = []string{"core", "modules", "cmd", "connectors", "sdk", "operator", "clients", "cloud", "commercial"}

// allowedDirectBinds names every file permitted to open a listening socket
// without sdk/netbind, and why. Each entry is a standing exposure decision: keep
// the list short, keep every reason true, and delete an entry the moment its
// file stops binding directly (the test enforces that).
var allowedDirectBinds = map[string]string{
	// The admission point itself. Someone has to call the standard library.
	"sdk/netbind/netbind.go": "IS the admission point.",

	// The SO_REUSEPORT listener factory (zero-downtime handover). It is a
	// listener CONSTRUCTOR in the same sense as net.Listen — it takes an address
	// somebody else chose and binds it with one socket option set. Admission
	// belongs at its CALLERS, which is why serverhandover.Listen is itself in the
	// scanner's detected set below: a caller that uses it is flagged exactly as if
	// it had called net.Listen.
	"core/serverhandover/handover.go": "listener factory; its callers are the ones that must admit.",

	// DECLARED RESIDUE (2026-08-08). The engine's serve path binds up to
	// eight listeners here and is the subject of an OPEN, UNMERGED pull request:
	// PR #565 (feature-s5-cifrado) rewrites serveHTTP to add its
	// own insecureBindGuard and an --insecure-allow-public-bind flag. Migrating
	// this file to netbind now would collide with that PR head-on, and was
	// scoped explicitly not to stack on it.
	//
	// This is the ONE exception on this list that is not permanent. When #565
	// lands, its guard and this package are the same policy written twice — the
	// exact duplication this test exists to prevent — and the follow-up is to
	// delete insecureBindGuard, route serveHTTP/serveGRPC through netbind, and
	// remove this entry.
	//
	// STATED PLAINLY, because a looser sentence here was wrong and an external
	// contrast caught it: on THIS branch, and on main, the engine's serve path has
	// NO plaintext refusal at all. The guard lives only on the unmerged branch of
	// #565. This entry is not "already covered elsewhere" — it is an OPEN exposure
	// on the mainline, deferred to avoid colliding with the PR that fixes it.
	"cmd/olivares/cmd_serve.go": "DECLARED RESIDUE: migrate to netbind and delete this entry; see the note above.",

	// Separate Go modules, outside go.work, with no dependency on the SDK module.
	// Wiring netbind into them means editing their go.mod/go.sum, which is a
	// change to a build this branch does not otherwise touch.
	//
	// NOT a clean bill of health — cloud-cp's metrics listener defaults to the
	// WILDCARD ":9090" in plaintext (cloud/control-plane/internal/config/config.go
	// METRICS_ADDR), which is the same class of defect fixed in the three
	// connectors. It is reported rather than changed because a metrics endpoint is
	// normally scraped from another container, so moving it to loopback could
	// break a live deployment; that is a call for the operator of that service,
	// not a side effect of this branch. commerce already defaults to loopback
	// (127.0.0.1:8790).
	"cloud/control-plane/cmd/cloud-cp/main.go": "separate module (no SDK dep); metrics default :9090 is a REPORTED wildcard.",
	"commercial/commerce/cmd/commerce/main.go": "separate module (no SDK dep); already defaults to loopback.",

	// The Kubernetes operator. controller-runtime's manager binds metrics (:8080)
	// and the health probe (:8081) itself, so no Listen call exists to route.
	//
	// The HEALTH PROBE genuinely must be reachable off-host: the kubelet probes the
	// pod IP, and a loopback bind would stop the operator passing its own liveness
	// and readiness checks. That one is correct as it stands.
	//
	// The METRICS endpoint is NOT justified, and an earlier version of this entry
	// claimed it was. It said the control here is a NetworkPolicy plus
	// controller-runtime's authn filter, "--metrics-secure is already wired".
	// Measured, after an external contrast disputed it: --metrics-secure defaults to
	// FALSE (operator/cmd/manager/main.go:56), so metrics serve PLAINTEXT on a
	// wildcard by default, and the only NetworkPolicy in the chart selects pods
	// labeled component: core, not the manager
	// (deploy/helm/olivares/templates/networkpolicy.yaml:18). Neither control is in
	// effect. Citing a protection that does not exist is worse than admitting the
	// gap, because it stops the next person looking.
	//
	// So this entry stands as an OPEN exposure of the same class this test exists to
	// catch, deferred rather than defended: the operator is a separate module and
	// changing its metrics posture is a deployment decision, not a side effect of
	// this branch.
	"operator/cmd/manager/main.go": "k8s operator: the health probe must be off-host; the metrics wildcard is an OPEN, undefended exposure.",
}

type directBind struct {
	file, call string
	line       int
}

func (d directBind) String() string {
	return d.file + ":" + strconv.Itoa(d.line) + " calls " + d.call
}

// scanForDirectSocketCalls walks the given trees and reports every production
// call that opens a listening socket.
//
// The detected set is deliberately broader than "what this tree happens to use
// today" — a sweep only answers what its SHAPE allows, and a scanner that knows
// only the idioms already present cannot catch the one someone reaches for next.
// tls.Listen appears here although it is currently test-only, for that reason.
func scanForDirectSocketCalls(t *testing.T, root string, dirs []string) []directBind {
	t.Helper()

	// Listener entry points, keyed by IMPORT PATH — never by the local identifier.
	//
	// Keying on the identifier is the obvious way to write this and it is wrong: a
	// file that says `import stdnet "net"` then calls `stdnet.Listen(...)` presents
	// a selector whose X is "stdnet", and a name-keyed scanner walks straight past
	// it. That was not hypothetical — this test was written that way first, and a
	// probe file containing exactly that alias with a wildcard bind passed the
	// invariant clean. So the file's own import table is resolved first and every
	// match is made against the path the alias points AT.
	qualified := map[string]map[string]bool{
		// Every net.Listen* in one rule, plus the ListenConfig TYPE (naming one is
		// how you bind while spelling it differently) and the File* constructors
		// (adopting an inherited descriptor is still opening a listening socket —
		// socket activation is exactly how a listener arrives without a Listen call).
		"net": {
			"Listen": true, "ListenPacket": true, "ListenTCP": true, "ListenUDP": true,
			"ListenIP": true, "ListenUnix": true, "ListenUnixgram": true,
			"ListenMulticastUDP": true, "ListenConfig": true,
			"FileListener": true, "FilePacketConn": true,
		},
		"crypto/tls":                 {"Listen": true, "NewListener": true},
		"github.com/quic-go/quic-go": {"Listen": true, "ListenAddr": true},
		"golang.org/x/net/netutil":   {"LimitListener": true},
		// A listener opened INSIDE a dependency. controller-runtime's manager binds
		// the metrics and health-probe addresses itself, so no Listen call appears
		// anywhere in operator/ and the syntactic scan walked past two wildcard
		// defaults. Found by the external the model contrast of this branch. It is
		// the honest bound of this test: see the note on its limits above.
		"sigs.k8s.io/controller-runtime":             {"NewManager": true},
		"sigs.k8s.io/controller-runtime/pkg/manager": {"New": true},
		// The in-tree SO_REUSEPORT factory: using it is binding.
		"github.com/olivaresai/olivares/core/serverhandover": {"Listen": true, "ListenPacket": true},
	}
	// Method names that bind whatever the receiver is (http.Server, http package,
	// grpc wrappers). Matched on the selector alone, deliberately.
	bareMethods := map[string]bool{
		"ListenAndServe": true, "ListenAndServeTLS": true,
	}

	var out []directBind
	for _, d := range dirs {
		base := filepath.Join(root, d)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				switch name := de.Name(); {
				case name == "node_modules", name == "testdata", name == "vendor", strings.HasPrefix(name, "."):
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Errorf("socket-admission: cannot read %s: %v — an unreadable file is not a clean one", path, rerr)
				return nil
			}
			if strings.Contains(string(src), "// Code generated") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, src, 0)
			if perr != nil {
				// A file the scanner cannot parse is a file it did not inspect.
				// Saying nothing here is how a sweep reports zero and means "blind".
				t.Errorf("socket-admission: cannot parse %s: %v — the invariant is UNVERIFIED for it", path, perr)
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)

			// Resolve this file's import table: local identifier -> import path.
			// A dot-import of a networking package would put Listen into the file
			// scope as a bare identifier and defeat the selector analysis entirely,
			// so it is reported rather than silently mis-analyzed.
			localToPath := map[string]string{}
			for _, imp := range f.Imports {
				p, uerr := strconv.Unquote(imp.Path.Value)
				if uerr != nil {
					continue
				}
				local := p
				if i := strings.LastIndex(p, "/"); i >= 0 {
					local = p[i+1:]
				}
				if imp.Name != nil {
					switch imp.Name.Name {
					case "_":
						continue
					case ".":
						if _, watched := qualified[p]; watched {
							pos := fset.Position(imp.Pos())
							out = append(out, directBind{file: rel, line: pos.Line, call: "dot-import of " + p + " (defeats this scan)"})
						}
						continue
					default:
						local = imp.Name.Name
					}
				}
				localToPath[local] = p
			}

			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				name := sel.Sel.Name
				if pkg, ok := sel.X.(*ast.Ident); ok {
					if path, imported := localToPath[pkg.Name]; imported {
						if names, known := qualified[path]; known && names[name] {
							pos := fset.Position(sel.Pos())
							out = append(out, directBind{file: rel, line: pos.Line, call: path + "." + name})
							return true
						}
					}
				}
				if bareMethods[name] {
					pos := fset.Position(sel.Pos())
					out = append(out, directBind{file: rel, line: pos.Line, call: "." + name})
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Errorf("socket-admission: walking %s: %v", base, err)
		}
	}
	return out
}

// TestSocketAdmissionScannerCatchesTheEvasions is the invariant's own test. The
// class guard above is only worth what its scanner can SEE, and a scanner is
// exactly the kind of code that silently stops seeing things.
//
// Every case here was a real hole first. The alias case in particular: this scan
// was originally keyed on the local identifier, and a probe file containing
// `import stdnet "net"` with a wildcard bind passed the invariant completely
// clean. That is the failure mode that matters — not a false alarm, a false
// silence — so it is pinned here rather than left to be rediscovered.
//
// It runs against a DECOY tree in t.TempDir(), never against the repository: a
// test that has to drop probe files into the live source tree to prove itself is
// one interrupted run away from leaving them there.
func TestSocketAdmissionScannerCatchesTheEvasions(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"plain", "package p\n\nimport \"net\"\n\nfunc f() { _, _ = net.Listen(\"tcp\", \":1\") }\n", "net.Listen"},
		{"import alias", "package p\n\nimport stdnet \"net\"\n\nfunc f() { _, _ = stdnet.Listen(\"tcp\", \":1\") }\n", "net.Listen"},
		{"dot import", "package p\n\nimport . \"net\"\n\nfunc f() { _, _ = Listen(\"tcp\", \":1\") }\n", "dot-import"},
		{"inherited fd", "package p\n\nimport (\n\t\"net\"\n\t\"os\"\n)\n\nfunc f() { _, _ = net.FileListener(os.Stdin) }\n", "net.FileListener"},
		{"listen config", "package p\n\nimport (\n\t\"context\"\n\t\"net\"\n)\n\nfunc f() { var lc net.ListenConfig; _, _ = lc.Listen(context.Background(), \"tcp\", \":1\") }\n", "net.ListenConfig"},
		{"http server", "package p\n\nimport \"net/http\"\n\nfunc f() { _ = (&http.Server{}).ListenAndServe() }\n", ".ListenAndServe"},
		{"tls", "package p\n\nimport (\n\t\"crypto/tls\"\n\t\"net\"\n)\n\nfunc f() net.Listener { l, _ := tls.Listen(\"tcp\", \":1\", nil); return l }\n", "crypto/tls.Listen"},
		{"reuseport factory", "package p\n\nimport (\n\t\"context\"\n\n\t\"github.com/olivaresai/olivares/core/serverhandover\"\n)\n\nfunc f() { _, _ = serverhandover.Listen(context.Background(), \"tcp\", \":1\") }\n", "serverhandover.Listen"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "decoy")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(c.src), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			found := scanForDirectSocketCalls(t, root, []string{"decoy"})
			if len(found) == 0 {
				t.Fatalf("the scanner did NOT see this bind; it would report a tree containing it as clean:\n%s", c.src)
			}
			var joined string
			for _, f := range found {
				joined += f.String() + "\n"
			}
			if !strings.Contains(joined, c.want) {
				t.Errorf("expected the finding to name %q, got:\n%s", c.want, joined)
			}
		})
	}

	// The other direction: a file that binds nothing must not be reported, or the
	// invariant becomes noise everyone learns to override.
	root := t.TempDir()
	dir := filepath.Join(root, "decoy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	clean := "package p\n\nimport (\n\t\"context\"\n\n\t\"github.com/olivaresai/olivares/sdk/netbind\"\n)\n\n// A fixture is read as an example, so it shows the shape a component should copy:\n// a loopback address and a policy that names itself and its opt-in.\nfunc f() {\n\t_, _ = netbind.Listen(context.Background(), \"tcp\", \"127.0.0.1:1\", netbind.Policy{Component: \"example\", Purpose: \"receiver\", OptIn: \"allow_public_bind\"})\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(clean), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if found := scanForDirectSocketCalls(t, root, []string{"decoy"}); len(found) != 0 {
		t.Errorf("a component going through the admission point must not be reported, got %v", found)
	}
}
