// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
)

// editionswitch.go is the one place that says how a Community build with a Business license
// becomes the Business build: the step for the way this installation was installed, read by
// `license install`, `license status` and the console's Updates page. The move itself is
// `olivares upgrade --edition business` (cmd_upgrade.go); nothing here moves anything.

// The ways an installation is installed, as the swap sees them.
const (
	installBinary     = "binary"     // a tarball or a copied binary: the file is the owner's
	installContainer  = "container"  // Docker or Podman: the image is read-only, the data volume is not
	installPackage    = "package"    // deb, rpm, apk, AUR: the package manager owns /usr/bin/olivares
	installKubernetes = "kubernetes" // a pod: the manifest rolls the image
)

// isInstalledPackage reports that this process is the package-owned binary. A package upgrade
// replaces the file under a running engine, which Linux then names "<path> (deleted)".
func isInstalledPackage() bool {
	exe, err := os.Executable()
	return err == nil && strings.TrimSuffix(exe, " (deleted)") == installedPackageBinary
}

// installKind is how this engine was installed, read from the same two facts `olivares upgrade`
// decides from (inContainer, installedPackageBinary), so the step it names is the step the
// upgrade command accepts.
func installKind() string {
	switch {
	case inContainer():
		return installContainer
	case os.Getenv("KUBERNETES_SERVICE_HOST") != "":
		return installKubernetes
	case isInstalledPackage():
		return installPackage
	}
	return installBinary
}

// isConnected reports that this data directory holds an approved connected identity, which
// fetches the licensed download's token by itself (cmd_license_connect.go). It reads the state
// the way `license connect` does (owner-only regular file, known schema). A state that
// `license connect start` wrote while the owner has not approved yet, a deleted binding and a
// file that cannot be read are not connected: the token route and its message apply.
func isConnected(dataDir string) bool {
	st, err := readConnectState(connectDir(dataDir))
	return err == nil && st != nil && st.Binding != nil && st.Binding.Status == "active"
}

// downloadTokenNeeded reports that the licensed download has no token to present: none in the
// environment and no connected identity to fetch one. The step then says where to get it.
func downloadTokenNeeded(dataDir string) bool {
	return strings.TrimSpace(osGetenv("OLIVARES_UPGRADE_TOKEN")) == "" && !isConnected(dataDir)
}

// tokenNote is the sentence a step carries while the licensed download has no token.
const tokenNote = " It needs a download token: set OLIVARES_UPGRADE_TOKEN (from the Downloads page at licenses.olivares.ai or your license email), " +
	"or run `olivares license connect start` once to connect this installation."

// editionSwitchStep is the next step to the Business build for one kind of installation.
// ConsoleAction says the engine can take the step itself (the console's button runs the same
// command and restarts); where it cannot, the console shows Command instead of a button.
// needsToken (downloadTokenNeeded) adds where the download token comes from, and takes the
// button away: the engine has no token to present, so the apply would only be refused.
func editionSwitchStep(kind string, needsToken bool) api.EditionSwitchDTO {
	s := editionSwitchBase(kind)
	if needsToken && s.Command != "" {
		s.Then += tokenNote
		s.ConsoleAction = false
	}
	return s
}

// switchStepFor is the step for one installation: editionSwitchStep with the token note while the
// download has no token. When that is because the connect state is there but cannot be used (open
// to others, an unknown schema), it names that state too: `license connect start` would meet the
// same refusal, and main surfaced it with its path.
func switchStepFor(kind, dataDir string) api.EditionSwitchDTO {
	needsToken := downloadTokenNeeded(dataDir)
	s := editionSwitchStep(kind, needsToken)
	if needsToken && s.Command != "" {
		if _, err := readConnectState(connectDir(dataDir)); errors.Is(err, errConnectStateUnsafe) {
			s.Then += " The connected-client state here cannot be used: " + err.Error() + "."
		}
	}
	return s
}

func editionSwitchBase(kind string) api.EditionSwitchDTO {
	s := api.EditionSwitchDTO{Install: kind}
	switch kind {
	case installContainer:
		s.Command = "docker compose exec -e OLIVARES_UPGRADE_TOKEN olivares olivares upgrade --edition business --yes"
		s.Then = "Then restart the container: docker compose restart olivares. The Business build is kept in the data volume and survives a recreate."
		s.ConsoleAction = true
	case installPackage:
		s.Command = "sudo --preserve-env=OLIVARES_UPGRADE_TOKEN olivares upgrade --edition business --target /usr/bin/olivares --data-dir /var/lib/olivares"
		s.Then = "Then restart the service. The package manager no longer describes that file, and a later package upgrade puts the Community build back: run this command again after it."
	case installKubernetes:
		s.Then = "A pod is declarative: roll the Business image through your manifest (docs.olivares.ai/how-to/install-a-license)."
	default:
		s.Install = installBinary
		s.Command = "olivares upgrade --edition business"
		s.Then = "Then restart the service to run the Business build. Your data stays where it is."
		s.ConsoleAction = true
	}
	return s
}

// commercialEdition is the edition a live license entitles ("" when it entitles none): the
// product name of its profile, Business unless the profile says Enterprise. An expired, invalid
// or missing license entitles nothing, so a Community build with one has no switch to offer.
func commercialEdition(d api.LicenseDisplayInfo) string {
	if d.Status != "valid" && d.Status != "perpetual" {
		return ""
	}
	profile := strings.ToLower(strings.TrimSpace(d.SupportProfile))
	if profile == "" {
		profile = strings.ToLower(strings.TrimSpace(d.Plan))
	}
	if profile == "enterprise" {
		return "enterprise"
	}
	return "business"
}

// withEdition names the edition a step moves to: what `license install`, `license status` and the
// console's update status all hand out.
func withEdition(s api.EditionSwitchDTO, to string) *api.EditionSwitchDTO {
	s.To = to
	return &s
}

// licensedEditionSwitch is the switch a license installed on THIS build offers: nil on a
// Business build, and on a license that is not live. status is the lifecycle status the caller
// printed (revocation included), so the step never contradicts it.
func licensedEditionSwitch(lic license.Verified, status license.Status, dataDir string) *api.EditionSwitchDTO {
	if thisEdition.name != "community" {
		return nil
	}
	to := commercialEdition(api.LicenseDisplayInfo{Status: string(status), Plan: lic.Plan(), SupportProfile: lic.SupportProfile()})
	if to == "" {
		return nil
	}
	return withEdition(switchStepFor(installKind(), dataDir), to)
}

// editionSwitchNote is the paragraph `license install` prints for a switch.
func editionSwitchNote(s *api.EditionSwitchDTO) string {
	name := "Business"
	if s.To == "enterprise" {
		name = "Enterprise"
	}
	note := fmt.Sprintf("This license is for the %s edition and this installation runs the Community build.", name)
	if s.Command != "" {
		note += "\nNext step: " + s.Command
	}
	return note + "\n" + s.Then
}
