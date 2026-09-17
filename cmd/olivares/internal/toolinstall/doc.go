// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package toolinstall installs official provider command-line tools into a
// versioned directory that Olivares owns, from publisher-signed release metadata,
// and records a typed receipt for each release.
//
// Invariants every provider adapter and the engine keep:
//
//   - Signed metadata is verified BEFORE any artifact byte is requested. The
//     verifier is an isolated GnuPG keyring holding only the key pinned in this
//     binary; a missing verifier or an invalid signature is a refusal, never a
//     fallthrough to download or execution.
//   - The plan selects concrete material: exact version, platform, URL, size and
//     SHA-256. Installing an approved plan re-resolves and refuses when the
//     selection changed; it never substitutes another artifact.
//   - Size and hash are checked before the artifact is made executable and before
//     it is probed. The probe runs the STAGED file from an empty temporary home
//     with a fixed minimal environment, in its own process group, under a finite
//     budget with SIGTERM then SIGKILL escalation, and is reaped.
//   - Releases live side by side under <root>/<driver>/<version>-<platform>.
//     Nothing overwrites or deletes an existing release. A failed or concurrent
//     install leaves prior releases byte-identical; only the staging directory
//     this operation created is removed on failure. Staging directories left by
//     other operations are reported, never removed by name.
//   - Staging is private (0700) while it is being filled; the finished, verified
//     release directory is set to 0755 just before it is renamed into place, so
//     a deliberately shared root can serve the binary. The operator's root and
//     existing releases are never chmod-ed.
//   - list and detect re-verify the retained signed manifest under the pin before
//     crediting a release as installed or registered; without a verifier they say
//     unverified, never installed. A damaged release is never executed. A probe
//     runs only for registered or manifest-corroborated candidates, or for an
//     exact path the operator named, and only when the file and its directory are
//     owned by the caller or root and writable by nobody else.
//   - Every destination path is opened through os.Root so a symlink planted under
//     the root cannot redirect a write outside it. The root must be a directory
//     the caller owns.
//   - No PATH, shell rc, HOME or credential file is read or edited. Detection only
//     inspects executables at the caller's own configured paths.
package toolinstall
