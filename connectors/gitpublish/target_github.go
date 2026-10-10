// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"strings"

	githubsrc "github.com/olivaresai/olivares/connectors/github"
)

type githubTargetKind struct{}
type githubTargetBinding struct {
	apiTargetBinding
	appID, installationID string
}

func (githubTargetKind) bind(cfg map[string]string) (TargetRow, error) {
	b := githubTargetBinding{apiTargetBinding: apiTarget(cfg, githubsrc.DefaultAPIBase), appID: strings.TrimSpace(cfg["app_id"]), installationID: strings.TrimSpace(cfg["installation_id"])}
	return targetRow(cfg, cfg["org"], b), nil
}
func (githubTargetKind) capabilities() TargetCapabilities {
	return TargetCapabilities{Effects: []Effect{EffectPush, EffectPullRequest, EffectMerge, EffectObserve, EffectRead}, MergeMethods: []string{"merge", "squash", "rebase"}, CIPaths: []string{".github/workflows"}, NarrowRead: true}
}
func (githubTargetKind) diffSource() DiffSource { return githubDiffSource{githubsrc.New()} }
func (b githubTargetBinding) open(repo TargetRepository, credential Secret, deps HostDependencies) (Host, error) {
	host, _, _ := strings.Cut(repo.ID, "/")
	return NewGitHub(GitHubConfig{APIBase: b.base, AllowedHosts: []string{host}, AppID: b.appID, InstallationID: b.installationID, Key: credential, Owner: repo.Owner, Repo: repo.Name}, deps.HTTP)
}
