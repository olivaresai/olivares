// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"strings"

	gitlabsrc "github.com/olivaresai/olivares/connectors/gitlab"
)

type gitlabTargetKind struct{}
type gitlabTargetBinding struct{ apiTargetBinding }

func (gitlabTargetKind) bind(cfg map[string]string) (TargetRow, error) {
	return targetRow(cfg, cfg["group"], gitlabTargetBinding{apiTarget(cfg, gitlabsrc.DefaultAPIBase)}), nil
}
func (gitlabTargetKind) capabilities() TargetCapabilities {
	return TargetCapabilities{Effects: []Effect{EffectPush, EffectPullRequest, EffectMerge, EffectObserve}, MergeMethods: []string{"merge", "squash"}, CIPaths: []string{".gitlab-ci.yml"}}
}
func (gitlabTargetKind) diffSource() DiffSource { return gitlabDiffSource{gitlabsrc.New()} }
func (b gitlabTargetBinding) open(repo TargetRepository, credential Secret, deps HostDependencies) (Host, error) {
	host, _, _ := strings.Cut(repo.ID, "/")
	return NewGitLab(GitLabConfig{APIBase: b.base, AllowedHosts: []string{host}, ProjectPath: repo.Owner + "/" + repo.Name, Token: credential}, deps.HTTP)
}
