// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package toolinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/catalog"
)

func TestDeclaredMCPInstallJourney(t *testing.T) {
	fixtureRepo := t.TempDir()
	if err := os.Mkdir(filepath.Join(fixtureRepo, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	// A faithful standalone stdio MCP server with an install-time version probe.
	program := "#!/usr/bin/python3\nimport sys,json\nif '--version' in sys.argv:\n print('fixture-mcp 1.0.0'); sys.exit(0)\nfor line in sys.stdin:\n r=json.loads(line); m=r.get('method'); result={'protocolVersion':'2024-11-05','capabilities':{'tools':{}},'serverInfo':{'name':'fixture-mcp','version':'1.0.0'}} if m=='initialize' else {'tools':[{'name':'echo','description':'Echo fixture','inputSchema':{'type':'object'}}]}\n if 'id' in r: print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':result}),flush=True)\n"
	if err := os.WriteFile(filepath.Join(fixtureRepo, "bin", "fixture-mcp"), []byte(program), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fixtureRepo, "lib", "fixture-mcp"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureRepo, "lib", "fixture-mcp", "resource.json"), []byte(`{"fixture":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "bin", "lib"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "MCP fixture"}, {"tag", "v1.0.0"}} {
		c := exec.Command("git", args...)
		c.Dir = fixtureRepo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, out)
		}
	}
	archiveCmd := exec.Command("git", "archive", "--format=tar.gz", "v1.0.0")
	archiveCmd.Dir = fixtureRepo
	archiveCmd.Stdout = &archive
	if err := archiveCmd.Run(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixture.tar.gz" {
			t.Errorf("unexpected network request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write(archive.Bytes())
	}))
	defer server.Close()
	declaration := catalog.ToolDeclaration{SourceURL: server.URL + "/fixture.tar.gz", Version: "1.0.0", SHA256: fmt.Sprintf("%x", sha256.Sum256(archive.Bytes())), Program: "fixture-mcp", ConfigHome: "fixture-mcp", Kind: "mcp"}
	engine := NewEngineWithCapabilities(nil, EngineOptions{HTTPClient: server.Client()})
	children := recordChildren(engine)
	req := RequestV2{Driver: "declared-fixture-mcp", Version: "1.0.0", Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "tools"), Declaration: &declaration}
	plan, err := engine.PlanV2(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := MarshalPlanV2(plan)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := ReadPlanV2(bytes.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := engine.InstallV2(context.Background(), req, approved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PlanDigest != plan.Digest || receipt.Tool == nil || receipt.Tool.Kind != "mcp" {
		t.Fatalf("receipt: %+v", receipt)
	}
	if _, err := os.Stat(receipt.Destination.Executable); err != nil {
		t.Fatal(err)
	}
	if len(*children) != 1 || len((*children)[0].rw) != 1 || !strings.HasSuffix((*children)[0].rw[0], "/.probe") {
		t.Fatalf("probe bypassed child confinement builder: %+v", *children)
	}
	mcp := exec.Command(receipt.Destination.Executable)
	mcp.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
	mcp.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n")
	if out, err := mcp.CombinedOutput(); err != nil || !bytes.Contains(out, []byte("protocolVersion")) || !bytes.Contains(out, []byte("echo")) {
		t.Fatalf("MCP handshake and tools/list: %v %s", err, out)
	}
	gitDeclaration := declaration
	gitDeclaration.SourceURL = "https://git.example/fixture-mcp.git"
	gitDeclaration.ArchiveURL = declaration.SourceURL
	req.Declaration = &gitDeclaration
	req.DestRoot = filepath.Join(t.TempDir(), "git-tools")
	gitPlan, err := engine.PlanV2(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.InstallV2(context.Background(), req, gitPlan, nil); err != nil {
		t.Fatal(err)
	}
	req.Declaration = &declaration
	req.DestRoot = plan.Selection.Destination.Root
	again, _, err := engine.InstallV2(context.Background(), req, approved, nil)
	if err != nil || again.InstalledAt != receipt.InstalledAt {
		t.Fatalf("replay: %v", err)
	}
	inv, err := engine.List(context.Background(), req.DestRoot)
	if err != nil || len(inv.Installed) != 1 || inv.Installed[0].State != StateInstalled {
		t.Fatalf("inventory: %+v %v", inv, err)
	}
	for _, field := range []string{"source_url", "config_home", "kind"} {
		altered := *receipt
		tool := *receipt.Tool
		altered.Tool = &tool
		switch field {
		case "source_url":
			tool.SourceURL = "https://other.example/release.tar.gz"
		case "config_home":
			tool.ConfigHome = "other-home"
		case "kind":
			tool.Kind = "cli"
		}
		b, err := json.Marshal(altered)
		if err != nil {
			t.Fatal(err)
		}
		receiptPath := filepath.Join(receipt.Destination.ReleaseDir, ReceiptFile)
		if err := os.WriteFile(receiptPath, b, 0644); err != nil {
			t.Fatal(err)
		}
		inv, err = engine.List(context.Background(), req.DestRoot)
		if err != nil || inv.Installed[0].State != StateDamaged {
			t.Fatalf("receipt %s tamper accepted: %+v %v", field, inv, err)
		}
		if _, _, err := engine.InstallV2(context.Background(), req, approved, nil); KindOf(err) != KindDamaged {
			t.Fatalf("receipt %s replay accepted: %v", field, err)
		}
	}
	original, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receipt.Destination.ReleaseDir, ReceiptFile), original, 0644); err != nil {
		t.Fatal(err)
	}
	resource := filepath.Join(receipt.Destination.ReleaseDir, "lib", "fixture-mcp", "resource.json")
	if err := os.Chmod(resource, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resource, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	inv, err = engine.List(context.Background(), req.DestRoot)
	if err != nil || inv.Installed[0].State != StateDamaged {
		t.Fatalf("resource tamper accepted: %+v %v", inv, err)
	}
	if _, _, err := engine.InstallV2(context.Background(), req, approved, nil); KindOf(err) != KindDamaged {
		t.Fatalf("damaged replay: %v", err)
	}
}

func TestDeclaredDigestMismatchWritesNothing(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("tampered")) }))
	defer server.Close()
	d := catalog.ToolDeclaration{SourceURL: server.URL + "/release.tar.gz", Version: "1.0.0", SHA256: strings.Repeat("a", 64), Program: "fixture-mcp", ConfigHome: "fixture-mcp", Kind: "mcp"}
	e := NewEngineWithCapabilities(nil, EngineOptions{HTTPClient: server.Client()})
	req := RequestV2{Driver: "declared-fixture-mcp", Version: d.Version, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "absent"), Declaration: &d}
	p, err := e.PlanV2(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = e.InstallV2(context.Background(), req, p, nil)
	if KindOf(err) != KindDigestMismatch {
		t.Fatalf("want digest_mismatch, got %v", err)
	}
	if _, err := os.Stat(req.DestRoot); !os.IsNotExist(err) {
		t.Fatalf("destination was written: %v", err)
	}
}

func TestDeclaredPlanRejectsUnadmittedTool(t *testing.T) {
	e := NewEngineWithCapabilities(nil, EngineOptions{})
	req := RequestV2{Driver: "declared-fixture-mcp", Version: "1.0.0", Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "absent")}
	_, err := e.PlanV2(context.Background(), req)
	if KindOf(err) != KindUnadmitted {
		t.Fatalf("want unadmitted_declaration, got %v", err)
	}
	if _, err := os.Stat(req.DestRoot); !os.IsNotExist(err) {
		t.Fatalf("destination was written: %v", err)
	}
}

func TestDeclaredGitMetadataKeepsArchiveGuards(t *testing.T) {
	for _, tc := range []struct {
		name       string
		extra      *tar.Header
		maxMembers int
		want       string
	}{
		{"member-limit", nil, 2, KindResponseTooLarge},
		{"link", &tar.Header{Name: "source-link", Typeflag: tar.TypeSymlink, Linkname: "/outside"}, 4096, KindManifestInvalid},
		{"special", &tar.Header{Name: "source-fifo", Typeflag: tar.TypeFifo}, 4096, KindManifestInvalid},
		{"traversal", &tar.Header{Name: "../outside", Typeflag: tar.TypeReg}, 4096, KindManifestInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload bytes.Buffer
			zw := gzip.NewWriter(&payload)
			tw := tar.NewWriter(zw)
			for range 2 {
				if err := tw.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "Git commit fixture"}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.WriteHeader(&tar.Header{Name: "bin/fixture-mcp", Typeflag: tar.TypeReg, Mode: 0755, Size: 4}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("MCP\n")); err != nil {
				t.Fatal(err)
			}
			if tc.extra != nil {
				if err := tw.WriteHeader(tc.extra); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "archive"), payload.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			layout := declaredLayout("fixture-mcp")
			layout.Limits.MaxMembers = tc.maxMembers
			_, err = extractReleaseArchive(context.Background(), "declared-fixture-mcp", root, "stage", "archive", layout)
			if KindOf(err) != tc.want {
				t.Fatalf("archive guard: want %s, got %v", tc.want, err)
			}
		})
	}
}
