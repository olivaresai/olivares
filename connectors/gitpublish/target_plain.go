// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import "strings"

type plainTargetKind struct{}
type plainTargetBinding struct{ remote PlainRemote }

func (plainTargetKind) bind(cfg map[string]string) (TargetRow, error) {
	remote, err := ParsePlainRemote(cfg[PublicationRemoteKey])
	if err != nil {
		return TargetRow{}, err
	}
	cut := strings.LastIndex(remote.Path, "/")
	return targetRow(cfg, remote.Path[:cut], plainTargetBinding{remote}), nil
}
func (plainTargetKind) capabilities() TargetCapabilities {
	return TargetCapabilities{Effects: []Effect{EffectPush, EffectObserve}, CIPaths: []string{".github/workflows", ".gitlab-ci.yml"}}
}
func (plainTargetKind) diffSource() DiffSource { return nil }
func (b plainTargetBinding) repository(path string) (TargetRepository, error) {
	if path != b.remote.Path {
		return TargetRepository{}, ErrTargetRepository
	}
	return targetRepository(b.remote.Host, path), nil
}
func (b plainTargetBinding) open(_ TargetRepository, credential Secret, deps HostDependencies) (Host, error) {
	return NewPlainGit(PlainGitConfig{Remote: b.remote.URL, Credential: credential}, deps.Git)
}
