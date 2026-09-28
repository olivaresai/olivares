// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The recipe's SHAPE gate. These tests: they read the description and the build inputs as data
// and refuse a recipe that would build the wrong thing. They build no image, start no
// container and boot nothing — that is the hosted runner's job
// (.github/workflows/appliance-image.yml), because an image build needs a container runtime
// and the boot oracle needs /dev/kvm, and neither exists where these tests run.

package images

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const repoRoot = "../.."

// The guest profiles are fixed here; the reviewed toolchain locks own the builder versions. Fedora 44
// is the shipping base (Root c1efc2ee); the Debian 13 profile stays, not shipping, with its own checks.
const (
	debianSuite   = "trixie"
	serverProfile = "fedora44-server-amd64"
	debianProfile = "debian13-server-amd64"
)

// kiwiImage is the subset of KIWI NG's schema this gate reads. Attribute and element names
// are KIWI's own (osinside.github.io/kiwi/image_description/elements.html).
type kiwiImage struct {
	XMLName       xml.Name `xml:"image"`
	SchemaVersion string   `xml:"schemaversion,attr"`
	Name          string   `xml:"name,attr"`
	DisplayName   string   `xml:"displayname,attr"`
	Description   struct {
		Specification string `xml:"specification"`
	} `xml:"description"`
	Profiles struct {
		Profile []struct {
			Name        string `xml:"name,attr"`
			Description string `xml:"description,attr"`
			Import      string `xml:"import,attr"`
		} `xml:"profile"`
	} `xml:"profiles"`
	Preferences []struct {
		Profiles       string `xml:"profiles,attr"`
		Arch           string `xml:"arch,attr"`
		Version        string `xml:"version"`
		PackageManager string `xml:"packagemanager"`
		Type           []struct {
			Image         string `xml:"image,attr"`
			Filesystem    string `xml:"filesystem,attr"`
			Firmware      string `xml:"firmware,attr"`
			EfiCSM        string `xml:"eficsm,attr"`
			InstallISO    string `xml:"installiso,attr"`
			InstallBoot   string `xml:"installboot,attr"`
			Format        string `xml:"format,attr"`
			KernelCmdline string `xml:"kernelcmdline,attr"`
		} `xml:"type"`
	} `xml:"preferences"`
	Repository []struct {
		Alias        string `xml:"alias,attr"`
		Profiles     string `xml:"profiles,attr"`
		Type         string `xml:"type,attr"`
		Distribution string `xml:"distribution,attr"`
		Components   string `xml:"components,attr"`
		Source       struct {
			Path string `xml:"path,attr"`
		} `xml:"source"`
	} `xml:"repository"`
	Packages []struct {
		Type     string `xml:"type,attr"`
		Profiles string `xml:"profiles,attr"`
		Package  []struct {
			Name string `xml:"name,attr"`
			Arch string `xml:"arch,attr"`
		} `xml:"package"`
		Archive []struct {
			Name string `xml:"name,attr"`
		} `xml:"archive"`
	} `xml:"packages"`
}

func read(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("the recipe does not deliver %s: %v", rel, err)
	}
	return string(data)
}

func description(t *testing.T) kiwiImage {
	t.Helper()
	var image kiwiImage
	if err := xml.Unmarshal([]byte(read(t, "appliance/images/kiwi/config.xml")), &image); err != nil {
		t.Fatalf("the image description is not the XML document KIWI reads: %v", err)
	}
	return image
}

// inProfile is KIWI's rule for an element's profiles attribute: without one, the element applies to
// every profile.
func inProfile(profiles, profile string) bool {
	return profiles == "" || slices.Contains(strings.Split(profiles, ","), profile)
}

// packagesFor collects the package names of one packages section type for a profile, and the
// sections with no profiles attribute, which apply to every profile.
func packagesFor(image kiwiImage, sectionType, profile string) []string {
	var names []string
	for _, section := range image.Packages {
		if section.Type != sectionType {
			continue
		}
		if !inProfile(section.Profiles, profile) {
			continue
		}
		for _, pkg := range section.Package {
			names = append(names, pkg.Name)
		}
	}
	return names
}

// TestRecipe_PinsKiwiAndDebian13 — the Debian profile names ONE tool version and ONE base, and
// takes its packages from Debian 13 and from the appliance's own packages, from nowhere else. An
// unpinned builder or a mirror nobody named is a build that cannot be repeated.
func TestRecipe_PinsKiwiAndDebian13(t *testing.T) {
	container := read(t, "appliance/images/toolchain/Containerfile")

	from := regexp.MustCompile(`(?m)^FROM\s+(\S+)`).FindAllStringSubmatch(container, -1)
	if len(from) != 1 {
		t.Fatalf("the build container must have exactly one base image, found %d", len(from))
	}
	if !strings.HasPrefix(from[0][1], "debian:13@sha256:") {
		t.Errorf("the build container is not Debian 13 pinned by digest: %q", from[0][1])
	}

	var lock struct {
		Toolchain struct {
			Kiwi struct {
				Version string `json:"upstream_version"`
			} `json:"kiwi"`
		} `json:"toolchain"`
	}
	if err := json.Unmarshal([]byte(read(t, "appliance/images/toolchain/input-lock.json")), &lock); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(lock.Toolchain.Kiwi.Version) {
		t.Fatal("the builder owner does not pin an exact KIWI version")
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "appliance/images/kiwi/Containerfile")); !os.IsNotExist(err) {
		t.Fatal("the recipe must not carry a second builder")
	}
	consumer := read(t, "appliance/images/kiwi/build.sh")
	for _, required := range []string{"toolchain/input-lock.json", "runner/admit.py", "consume.py", "--image-id", "--accelerator"} {
		if !strings.Contains(consumer, required) {
			t.Errorf("the recipe does not consume the admitted builder: %q", required)
		}
	}

	image := description(t)
	if !strings.Contains(image.Description.Specification, "Debian 13") {
		t.Errorf("the description does not say the appliance is based on Debian 13: %q", image.Description.Specification)
	}
	if len(image.Repository) == 0 {
		t.Fatal("the description declares no repository")
	}
	debianHosts := 0
	for _, repo := range image.Repository {
		if !inProfile(repo.Profiles, debianProfile) {
			continue
		}
		path := repo.Source.Path
		switch {
		case strings.HasPrefix(path, "obs://") || strings.HasPrefix(path, "obsrepositories:/"):
			t.Errorf("repository %q takes packages from the Open Build Service, not from Debian: %q", repo.Alias, path)
		case strings.HasPrefix(path, "dir://") || strings.HasPrefix(path, "file://"):
			// The appliance's own packages, staged into the build by build.sh.
			if repo.Type != "deb-dir" {
				t.Errorf("the local package repository %q must be a deb-dir, it is %q", repo.Alias, repo.Type)
			}
			continue
		default:
			debianHosts++
			if repo.Type != "apt-deb" {
				t.Errorf("repository %q is not an apt repository: %q", repo.Alias, repo.Type)
			}
			if !strings.HasPrefix(path, "http://deb.debian.org/") && !strings.HasPrefix(path, "http://security.debian.org/") &&
				!strings.HasPrefix(path, "https://deb.debian.org/") && !strings.HasPrefix(path, "https://security.debian.org/") {
				t.Errorf("repository %q is not a Debian mirror: %q", repo.Alias, path)
			}
			if !strings.HasPrefix(repo.Distribution, debianSuite) {
				t.Errorf("repository %q is not Debian 13 %q: %q", repo.Alias, debianSuite, repo.Distribution)
			}
		}
	}
	if debianHosts == 0 {
		t.Error("the description declares no Debian mirror")
	}
}

// TestRecipe_PinsKiwiAndFedora44 — the shipping profile names ONE tool version and ONE base: the
// Fedora 44 builder pinned by digest with KIWI 11.0.4, and packages from the Fedora 44 releases and
// updates trees as rpm-md and from the appliance's own RPMs, from nowhere else.
func TestRecipe_PinsKiwiAndFedora44(t *testing.T) {
	container := read(t, "appliance/images/toolchain/fedora44/Containerfile")
	from := regexp.MustCompile(`(?m)^FROM\s+(\S+)`).FindAllStringSubmatch(container, -1)
	if len(from) != 1 {
		t.Fatalf("the Fedora build container must have exactly one base image, found %d", len(from))
	}
	if !strings.HasPrefix(from[0][1], "registry.fedoraproject.org/fedora:44@sha256:") {
		t.Errorf("the Fedora build container is not Fedora 44 pinned by digest: %q", from[0][1])
	}
	var lock struct {
		Toolchain struct {
			Kiwi struct {
				Version string `json:"upstream_version"`
			} `json:"kiwi"`
		} `json:"toolchain"`
	}
	if err := json.Unmarshal([]byte(read(t, "appliance/images/toolchain/fedora44/input-lock.json")), &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Toolchain.Kiwi.Version != "11.0.4" {
		t.Errorf("the Fedora builder does not pin KIWI 11.0.4: %q", lock.Toolchain.Kiwi.Version)
	}

	image := description(t)
	if !strings.Contains(image.Description.Specification, "based on Fedora 44") {
		t.Errorf("the description does not say the appliance is based on Fedora 44: %q", image.Description.Specification)
	}
	fedoraTrees := 0
	for _, repo := range image.Repository {
		if !inProfile(repo.Profiles, serverProfile) {
			continue
		}
		path := repo.Source.Path
		if repo.Type != "rpm-md" {
			t.Errorf("repository %q of the Fedora profile is not rpm-md: %q", repo.Alias, repo.Type)
		}
		switch {
		case strings.HasPrefix(path, "dir://"):
			// The appliance's own RPMs, staged into the build by build.sh.
		case strings.HasPrefix(path, "https://dl.fedoraproject.org/pub/fedora/linux/releases/44/"),
			strings.HasPrefix(path, "https://dl.fedoraproject.org/pub/fedora/linux/updates/44/"):
			fedoraTrees++
		default:
			t.Errorf("repository %q of the Fedora profile is not a Fedora 44 tree: %q", repo.Alias, path)
		}
	}
	if fedoraTrees != 2 {
		t.Errorf("the Fedora profile reads %d Fedora 44 trees, not the releases and updates trees", fedoraTrees)
	}
}

// TestRecipe_ServerProfileInstallsTheLayerAndFirstBoot — the one description carries the
// server amd64 profile, and that profile installs the product and the appliance layer as
// PACKAGES. The layer's postinstall is what enables first boot; the recipe never starts a
// service, because every identity is made on the first boot of each instance (design 4.1).
func TestRecipe_ServerProfileInstallsTheLayerAndFirstBoot(t *testing.T) {
	image := description(t)

	// The product, the layer (first boot, the console label, the answers tool), the owner of the
	// host settings, the host keys, the kernel and PID 1, the Secure Boot chain (shim signed by the
	// Microsoft UEFI CA, then the signed GRUB it verifies), the legacy BIOS path of the same disk
	// (KIWI eficsm), the root file system, and the initrd KIWI builds on a non-SUSE base; on
	// Fedora also the SELinux policy, enforcing.
	profiles := map[string][]string{
		serverProfile: {"olivares", "olivares-appliance-base", "cloud-init", "openssh-server", "kernel", "systemd",
			"shim-x64", "grub2-efi-x64", "grub2-pc", "btrfs-progs", "dracut", "selinux-policy-targeted"},
		debianProfile: {"olivares", "olivares-appliance-base", "cloud-init", "openssh-server", "linux-image-amd64",
			"systemd-sysv", "shim-signed", "grub-efi-amd64-signed", "grub-pc-bin", "btrfs-progs", "dracut"},
	}
	firmware := map[string][]string{
		serverProfile: {"linux-firmware"},
		debianProfile: {"firmware-linux-nonfree", "firmware-misc-nonfree"},
	}
	for name, required := range profiles {
		found := false
		for _, profile := range image.Profiles.Profile {
			if profile.Name == name {
				found = true
				if profile.Description == "" {
					t.Errorf("the %s profile has no description", name)
				}
			}
		}
		if !found {
			t.Fatalf("the description has no %s profile", name)
		}
		installed := packagesFor(image, "image", name)
		for _, pkg := range required {
			if !slices.Contains(installed, pkg) {
				t.Errorf("the %s profile does not install %q", name, pkg)
			}
		}
		for _, pkg := range firmware[name] {
			if slices.Contains(installed, pkg) {
				t.Errorf("firmware belongs to the FIRMWARE flag's own profile, not to %s: %q", name, pkg)
			}
		}
	}

	prefs := 0
	for _, pref := range image.Preferences {
		if !inProfile(pref.Profiles, serverProfile) {
			continue
		}
		if pref.Arch != "x86_64" {
			t.Errorf("the preferences of %s are not amd64 (x86_64): %q", serverProfile, pref.Arch)
		}
		if pref.PackageManager != "dnf5" {
			t.Errorf("the %s profile does not install with dnf5: %q", serverProfile, pref.PackageManager)
		}
		for _, typ := range pref.Type {
			prefs++
			if typ.Image != "oem" {
				t.Errorf("the image type is %q, not the expandable disk the formats are assembled from", typ.Image)
			}
			if typ.Firmware != "uefi" {
				t.Errorf("the firmware is %q; uefi is the EFI layout with Secure Boot", typ.Firmware)
			}
			if typ.EfiCSM == "false" {
				t.Error("eficsm is off, so the same disk cannot boot under legacy BIOS")
			}
			if typ.InstallISO != "true" {
				t.Error("the type does not produce the installer ISO")
			}
			if !strings.Contains(typ.KernelCmdline, "console=ttyS0") {
				t.Errorf("the kernel command line does not put a console on the serial port, where the boot battery reads the label: %q", typ.KernelCmdline)
			}
		}
	}
	if prefs != 1 {
		t.Errorf("the %s profile declares %d image types, it must declare exactly one", serverProfile, prefs)
	}

	// The image is built without ever starting the product or first boot, and without any of
	// first boot's work moving into a recipe script: the layer's postinstall is what enables
	// the unit, and the product's own generators run on the first boot of each instance.
	for _, rel := range []string{"appliance/images/kiwi/config.sh", "appliance/images/kiwi/build.sh"} {
		body := read(t, rel)
		for _, forbidden := range []string{
			"systemctl start", "systemctl enable", "systemctl --now",
			"olivares config generate", "olivares setup", "olivares db init",
			"appliance-firstboot apply",
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s does the layer's or first boot's work: %q", rel, forbidden)
			}
		}
	}
	// What the recipe does own is the template: an image carries no instance identity, and
	// the layer's own gate says so over the finished root.
	config := read(t, "appliance/images/kiwi/config.sh")
	for _, required := range []string{"/etc/machine-id", "ssh_host_", "appliance-firstboot check-template"} {
		if !strings.Contains(config, required) {
			t.Errorf("config.sh does not leave a template: %q is missing", required)
		}
	}
}

// TestRecipe_NoForbiddenNameInProductStrings — the product's name never contains "Debian" or
// "Linux" (owner decision, design 10.3), nor "Fedora", a trademark of the Fedora Project like the
// other two; "based on Fedora 44" as a statement of the base is what the description says instead.
// The build profile's own name is a build input, not a product string, and is not read here.
func TestRecipe_NoForbiddenNameInProductStrings(t *testing.T) {
	image := description(t)
	formats := formatsPolicy(t)

	productStrings := map[string]string{
		"config.xml image@name":        image.Name,
		"config.xml image@displayname": image.DisplayName,
		"formats.json product":         formats.Product,
		"formats.json vendor":          formats.Vendor,
	}
	for _, artifact := range formats.Artifacts {
		productStrings["formats.json file "+artifact.Format] = artifact.File
	}
	for where, value := range productStrings {
		lower := strings.ToLower(value)
		for _, forbidden := range []string{"debian", "linux", "fedora"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("%s is a product string and contains %q: %q", where, forbidden, value)
			}
		}
	}
	if formats.Product == "" {
		t.Error("the formats policy declares no product name")
	}
	if !strings.Contains(image.Description.Specification, "based on Fedora 44") {
		t.Errorf("the description does not state the base the way the owner allows: %q", image.Description.Specification)
	}
}
