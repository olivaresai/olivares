// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package carriers_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/answers"
	"github.com/olivaresai/olivares/appliance/answers/carriers"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// writeProtected places data where a carrier reads it, with the mode the real owner uses.
func writeProtected(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// cloudInitWrites performs the seed's write_files entry for path, as cloud-init does from
// the NoCloud seed. The seed is JSON-shaped cloud-config (JSON is YAML), so the standard
// library reads the same bytes cloud-init reads.
func cloudInitWrites(t *testing.T, userData []byte, path string) []byte {
	t.Helper()
	body, ok := bytes.CutPrefix(userData, []byte("#cloud-config\n"))
	if !ok {
		t.Fatal("user-data is not cloud-config")
	}
	var config struct {
		WriteFiles []struct {
			Path, Encoding, Content string
		} `json:"write_files"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	for _, f := range config.WriteFiles {
		if f.Path == path && f.Encoding == "b64" {
			data, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
	}
	t.Fatalf("the seed writes no %s", path)
	return nil
}

// ovfEnvironment answers the two VMware tool invocations the guestinfo carrier makes.
func ovfEnvironment(env []byte, argv *[]string) carriers.Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if argv != nil {
			*argv = append(*argv, append([]string{name}, args...)...)
		}
		if name == "vmware-rpctool" && len(args) == 1 && args[0] == "info-get guestinfo.ovfEnv" {
			return env, nil
		}
		return nil, errors.New("no such tool")
	}
}

// allCarriers places doc in every carrier the way its real owner delivers it.
func allCarriers(t *testing.T, doc []byte) []carriers.Carrier {
	t.Helper()
	root := t.TempDir()
	nocloud := filepath.Join(root, carriers.NoCloudPath)
	writeProtected(t, nocloud, cloudInitWrites(t, readFixture(t, "nocloud/user-data"), carriers.NoCloudPath), 0o600)
	if !bytes.Equal(doc, readFixture(t, "answers.json")) {
		writeProtected(t, nocloud, doc, 0o600)
	}
	env := readFixture(t, "guestinfo/ovf-env.xml")
	env = bytes.Replace(env, []byte(base64.StdEncoding.EncodeToString(readFixture(t, "answers.json"))), []byte(base64.StdEncoding.EncodeToString(doc)), 1)
	credentials := filepath.Join(root, "run/credentials/olivares-appliance-firstboot.service")
	writeProtected(t, filepath.Join(credentials, carriers.CredentialName), doc, 0o400)
	file := filepath.Join(root, carriers.FilePath)
	writeProtected(t, file, doc, 0o644)
	return []carriers.Carrier{
		carriers.FileCarrier{Label: carriers.NoCloud, Path: nocloud},
		carriers.GuestInfoCarrier{Run: ovfEnvironment(env, nil)},
		carriers.CredentialCarrier{Dir: credentials},
		carriers.FileCarrier{Label: carriers.File, Path: file},
	}
}

func canonicalPlan(t *testing.T, doc []byte) []byte {
	t.Helper()
	plan, err := answers.Build(bytes.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCarriers_EveryCarrierEmitsTheSameDocumentForTheSameInput(t *testing.T) {
	want := readFixture(t, "answers.json")
	wantPlan := canonicalPlan(t, want)
	all := allCarriers(t, want)
	for _, c := range all {
		t.Run(c.Name(), func(t *testing.T) {
			d, ok, err := c.Read(context.Background())
			if err != nil || !ok {
				t.Fatalf("carrier read: ok=%v err=%v", ok, err)
			}
			if d.Carrier != c.Name() {
				t.Fatalf("delivery names carrier %q, want %q", d.Carrier, c.Name())
			}
			if !bytes.Equal(d.Document, want) {
				t.Fatalf("%s changed the document it carried", d)
			}
			if !bytes.Equal(canonicalPlan(t, d.Document), wantPlan) {
				t.Fatalf("%s yields a different plan", d)
			}
		})
	}
	got, err := carriers.Resolve(context.Background(), "", all...)
	if err != nil {
		t.Fatalf("four agreeing carriers were refused: %v", err)
	}
	if !bytes.Equal(got.Document, want) {
		t.Fatal("resolution changed the document")
	}
	if _, err := carriers.Resolve(context.Background(), ""); !errors.Is(err, carriers.ErrNoCarrier) {
		t.Fatalf("no carrier must be reported as absent, got %v", err)
	}
}

func TestCarriers_ConflictingSourcesRefuseNamingSourcesWithoutSecrets(t *testing.T) {
	want := readFixture(t, "answers.json")
	other := bytes.Replace(want, []byte(`"hostname": "olivares.example.test"`), []byte(`"hostname": "do-not-print.example.test"`), 1)
	agreeing := allCarriers(t, want)
	disagreeing := allCarriers(t, other)
	// Credential and guestinfo carry one document, the local file another.
	mixed := []carriers.Carrier{agreeing[1], agreeing[2], disagreeing[3]}
	_, err := carriers.Resolve(context.Background(), "", mixed...)
	var conflict *carriers.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("disagreeing carriers were not refused: %v", err)
	}
	if strings.Join(conflict.Carriers, ",") != "file,guestinfo,systemd-credential" {
		t.Fatalf("refusal names %v", conflict.Carriers)
	}
	for _, leaked := range []string{"do-not-print", "olivares.example.test", "ssh-ed25519", "AAAAC3"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("refusal carries document content %q: %v", leaked, err)
		}
	}
	// A formatting difference is not a conflict: carriers are compared by canonical plan.
	var compact bytes.Buffer
	if err := json.Compact(&compact, want); err != nil {
		t.Fatal(err)
	}
	reformatted := allCarriers(t, compact.Bytes())
	if _, err := carriers.Resolve(context.Background(), "", agreeing[2], reformatted[3]); err != nil {
		t.Fatalf("equivalent documents were refused: %v", err)
	}
	// An explicit selection resolves the disagreement to the named carrier only.
	got, err := carriers.Resolve(context.Background(), carriers.File, mixed...)
	if err != nil || got.Carrier != carriers.File || !bytes.Equal(got.Document, other) {
		t.Fatalf("explicit selection: %v %v", got, err)
	}
	if _, err := carriers.Resolve(context.Background(), carriers.NoCloud, mixed...); !errors.Is(err, carriers.ErrNoCarrier) {
		t.Fatalf("a selected carrier that holds nothing must be absent, got %v", err)
	}
	selection := filepath.Join(t.TempDir(), "carrier")
	writeProtected(t, selection, []byte("file\n"), 0o644)
	if name, err := carriers.Selection(selection); err != nil || name != carriers.File {
		t.Fatalf("selection file: %q %v", name, err)
	}
	writeProtected(t, selection, []byte("nocloud,file\n"), 0o644)
	if _, err := carriers.Selection(selection); err == nil {
		t.Fatal("an ambiguous selection was accepted")
	}
}

func TestCarriers_SecretsAreReferencesNeverLiteralsInStatusArgvOrLogs(t *testing.T) {
	doc := readFixture(t, "answers.json")
	encoded := base64.StdEncoding.EncodeToString(doc)
	content := []string{"olivares.example.test", "AAAAC3NzaC1lZDI1NTE5", encoded[:24]}

	// Status records and logs name a delivery by carrier and reference only.
	for _, c := range allCarriers(t, doc) {
		d, _, err := c.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		label := d.String()
		if label != d.Carrier+":"+d.Ref {
			t.Fatalf("delivery label %q is not carrier:reference", label)
		}
		for _, leaked := range content {
			if strings.Contains(label, leaked) {
				t.Fatalf("delivery label carries content: %q", label)
			}
		}
	}

	// The guestinfo carrier's argument vectors name the key; the value arrives on stdout.
	var argv []string
	env := readFixture(t, "guestinfo/ovf-env.xml")
	if _, ok, err := (carriers.GuestInfoCarrier{Run: ovfEnvironment(env, &argv)}).Read(context.Background()); !ok || err != nil {
		t.Fatalf("guestinfo read: %v %v", ok, err)
	}
	if strings.Join(argv, " ") != "vmware-rpctool info-get guestinfo.ovfEnv" {
		t.Fatalf("unexpected argv %q", argv)
	}
	for _, arg := range argv {
		for _, leaked := range content {
			if strings.Contains(arg, leaked) {
				t.Fatalf("argv carries content: %q", arg)
			}
		}
	}

	// A document with a literal secret is refused by schema path, without its value.
	secret := bytes.Replace(doc, []byte(`"node_role": "control"`), []byte(`"node_role": "control", "password": "DO-NOT-PRINT"`), 1)
	_, err := carriers.Resolve(context.Background(), "", allCarriers(t, secret)[2])
	var invalid *carriers.InvalidError
	var schema *answers.ValidationError
	if !errors.As(err, &invalid) || !errors.As(err, &schema) || strings.Contains(err.Error(), "DO-NOT-PRINT") {
		t.Fatalf("secret literal: %v", err)
	}

	// Inputs that are not protected regular files are refused by reference, unread.
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	writeProtected(t, target, secret, 0o600)
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(dir, "loose.json")
	writeProtected(t, loose, secret, 0o666)
	large := filepath.Join(dir, "large.json")
	writeProtected(t, large, bytes.Repeat([]byte(" "), carriers.MaxDocumentBytes+1), 0o600)
	for _, path := range []string{link, loose, large} {
		_, _, err := (carriers.FileCarrier{Label: carriers.File, Path: path}).Read(context.Background())
		var input *carriers.InputError
		if !errors.As(err, &input) || input.Ref != path || strings.Contains(err.Error(), "DO-NOT-PRINT") {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
	}
}
