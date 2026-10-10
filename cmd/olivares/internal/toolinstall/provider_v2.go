// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"io"
	"io/fs"
	"os"
)

// PackageProviderV2 is the Codex/Grok capability. Claude implements Provider
// only and must not grow Place, FetchV2, or a success stub of this interface.
//
// stagingRel and fetchedRel are relative to the engine's *os.Root. PlaceV2 and
// VerifyPayload do not receive host paths. OpenFile opens from the owning root
// and returns a regular file whose identity is confirmed with Lstat, Stat and
// os.SameFile; the handle stays open until the operation finishes. Callers
// must not type-assert from io.ReadCloser.
type PackageProviderV2 interface {
	Key() string
	ResolveV2(context.Context, RequestV2) (*PlanV2, *ResolvedMaterialV2, error)
	FetchV2(context.Context, *PlanV2, io.Writer) (FetchedObjectObserved, error)
	PlaceV2(context.Context, *PlanV2, *os.Root, string, string) (*ObservedPayloadInventory, error)
	VerifyPayload(context.Context, PayloadAccess, *ObservedPayloadInventory, PackagePolicyV2) error
	Probe(context.Context, string, string, string) (ProbeReport, error)
	DefaultPaths(string) []string
}

// releaseRoutes is implemented by a v2 adapter that can name the pointer its plan
// for a release records when a person selects that release another way: by its
// exact version, latest or stable. Revalidation uses it to recognize an installed
// release reached through another route (sameReleaseOtherRoute).
type releaseRoutes interface {
	routePointer(sel SelectionV2, channel string) string
}

// releaseChecksumRoutes is implemented by a v2 adapter whose release is stated by
// more than one official checksum document (release_archive.go: the API metadata and
// the release's own checksum file); revalidation tries each of them.
type releaseChecksumRoutes interface {
	otherChecksums(sel SelectionV2) []URLRef
}

// PayloadAccess is the confined view VerifyPayload uses to inspect placed
// members. Lstat and OpenFile take paths relative to the owning *os.Root.
type PayloadAccess interface {
	Lstat(string) (fs.FileInfo, error)
	OpenFile(string) (*os.File, error)
}
