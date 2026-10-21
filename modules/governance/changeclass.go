// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The class of a change a publication proposes, by the paths it touches. An
// approval policy may match on it (approvalMatch.ChangeClass) and raise the
// tier or the quorum, or require someone other than the launcher.
const (
	ChangeClassDocsOnly  = "docs_only"
	ChangeClassCode      = "code"
	ChangeClassSensitive = "sensitive"
)

// maxChangePaths bounds the paths an approval records and shows, and the
// globs a policy lists.
const maxChangePaths = 50

// maxShownPath bounds one path as it is recorded and shown.
const maxShownPath = 256

// maxGlobStars bounds the "**" segments of one glob, which bounds its match
// work.
const maxGlobStars = 2

// defaultSensitivePaths is the sensitive set. The globs a tenant's approval
// policies list are added to it, never taken from it. Matching ignores case.
// It covers CI and release configuration, ownership, dependency manifests and
// lockfiles, build recipes, schema migrations, and the files that steer other
// agents and tools.
var defaultSensitivePaths = []string{
	// CI and release configuration, ownership
	".github/workflows/**", ".github/actions/**", ".github/dependabot.yml", ".gitlab-ci.yml", ".gitlab/**",
	".circleci/**", "Jenkinsfile", "azure-pipelines.yml", "CODEOWNERS",
	// dependency manifests and lockfiles
	"package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", ".npmrc",
	"go.mod", "go.sum", "go.work", "go.work.sum", "Cargo.toml", "Cargo.lock",
	"pyproject.toml", "setup.py", "poetry.lock", "uv.lock", "Pipfile", "Pipfile.lock", "requirements*.txt",
	"Gemfile", "Gemfile.lock", "composer.json", "composer.lock",
	"pom.xml", "build.gradle", "build.gradle.kts", "gradle.lockfile",
	// build recipes and schema migrations
	"Dockerfile*", "*.Dockerfile", "docker-compose*.yml", "Makefile", "**/migrations/**",
	// repository behavior and the files that steer other agents and tools
	".gitmodules", ".gitattributes", ".pre-commit-config.yaml", ".husky/**",
	"AGENTS.md", "CLAUDE.md", "GEMINI.md", "CODEX.md", ".claude/**", ".cursor/**", ".codex/**", ".github/copilot-instructions.md",
}

// docsPaths are the paths that are documentation and nothing else: prose by
// extension, whatever the directory, and licence files. Each is matched against
// the whole path, so a directory named like a document makes nothing in it one.
var docsPaths = []string{"**/*.md", "**/*.mdx", "**/*.rst", "**/*.adoc", "LICENSE*", "NOTICE*", "CHANGELOG*"}

// ChangeClassification is the class of a change and the paths that decided it:
// the sensitive paths for sensitive, the non-documentation paths for code and
// every path for docs_only. Paths are bounded and printable; they are never
// diff content. More counts the paths left out of Paths by the bound.
type ChangeClassification struct {
	Class string
	Paths []string
	More  int
}

// validChangeClass reports whether v is one of the three classes.
func validChangeClass(v string) bool {
	return v == ChangeClassDocsOnly || v == ChangeClassCode || v == ChangeClassSensitive
}

// validGlob reports whether pattern is a usable change-path glob: relative to
// the repository root, no "." or ".." segment and no empty one.
func validGlob(pattern string) bool {
	if pattern == "" || len(pattern) > maxMatchLen || strings.HasPrefix(pattern, "/") || strings.HasSuffix(pattern, "/") ||
		strings.Count(pattern, "**") > maxGlobStars || containsInlineCredential(pattern) {
		return false
	}
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		if _, err := path.Match(seg, ""); err != nil {
			return false
		}
	}
	return true
}

// globMatch matches the whole repository path p against pattern, segment by
// segment: a "**" segment spans any number of directories (a trailing one needs
// something below).
func globMatch(pattern, p string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return len(segs) > 0
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// treeMatch is the .gitignore reading of a sensitive glob, ignoring case: a
// pattern with no slash names a file or a directory at any depth, one with a
// slash is anchored at the repository root, and either matches everything
// below a directory it names.
func treeMatch(pattern, p string) bool {
	pattern, p = strings.ToLower(pattern), strings.ToLower(p)
	segs := strings.Split(p, "/")
	if !strings.Contains(pattern, "/") {
		for _, seg := range segs {
			if ok, err := path.Match(pattern, seg); err == nil && ok {
				return true
			}
		}
		return false
	}
	pat := strings.Split(pattern, "/")
	for k := 1; k <= len(segs); k++ {
		if matchSegments(pat, segs[:k]) {
			return true
		}
	}
	return false
}

// abnormalPath reports a path no checkout would produce: an empty, "." or ".."
// segment, or one inside a .git directory. It cannot be told from what it
// pretends to be, so it counts as sensitive.
func abnormalPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.EqualFold(seg, ".git") {
			return true
		}
	}
	return false
}

func sensitivePath(globs []string, p string) bool {
	if abnormalPath(p) {
		return true
	}
	for _, g := range globs {
		if treeMatch(g, p) {
			return true
		}
	}
	return false
}

func docsPath(p string) bool {
	for _, g := range docsPaths {
		if globMatch(g, p) {
			return true
		}
	}
	return false
}

// shownPath is a path as the card, the ledger and the review text show it:
// secret-shaped text masked, anything a reader could misread (control, format,
// line and paragraph separators, bytes that are not UTF-8) written out as
// escapes, and a long one cut.
func shownPath(p string) string {
	p = redact.Clean(p)
	if !utf8.ValidString(p) || strings.IndexFunc(p, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	}) >= 0 {
		p = strconv.QuoteToGraphic(p)
	}
	if len(p) > maxShownPath {
		p = strings.ToValidUTF8(p[:maxShownPath], "") + "…"
	}
	return p
}

// classifyChange is the class of paths against the sensitive globs: sensitive
// when any path matches (or is abnormal), code when any path is not
// documentation, else docs_only (an empty change is vacuously so). A rename
// arrives as both of its paths, so moving a file into or out of a sensitive
// place is sensitive.
func classifyChange(paths, sensitive []string) ChangeClassification {
	var hit, code []string
	for _, p := range paths {
		switch {
		case sensitivePath(sensitive, p):
			hit = append(hit, p)
		case !docsPath(p):
			code = append(code, p)
		}
	}
	class, shown := ChangeClassDocsOnly, paths
	switch {
	case len(hit) > 0:
		class, shown = ChangeClassSensitive, hit
	case len(code) > 0:
		class, shown = ChangeClassCode, code
	}
	out := ChangeClassification{Class: class, Paths: []string{}}
	for i, p := range shown {
		if i == maxChangePaths {
			out.More = len(shown) - i
			break
		}
		out.Paths = append(out.Paths, shownPath(p))
	}
	return out
}

// appliesToAction reports whether an approval policy governs action: an empty
// match action is every action.
func (s approvalSpec) appliesToAction(action string) bool {
	return s.Match.Action == "" || s.Match.Action == action
}

// changeClassPolicies parses the tenant's enabled approval policies that govern
// action and use what only Business offers. A policy that cannot be read is an
// error: a classification that quietly skipped it could run under a lower tier
// than its author set.
func changeClassPolicies(pols []model.Policy, action string) ([]approvalSpec, error) {
	var out []approvalSpec
	for _, p := range pols {
		spec, err := parseApprovalSpec(p.Spec)
		if err != nil {
			return nil, errors.New("an approval policy is unreadable")
		}
		if spec.appliesToAction(action) && spec.usesChangeClass() {
			out = append(out, spec)
		}
	}
	return out, nil
}

// sensitiveGlobs is the tenant's sensitive set for action: the defaults and
// the sensitive_paths of its enabled approval policies.
func sensitiveGlobs(specs []approvalSpec) []string {
	out := append([]string(nil), defaultSensitivePaths...)
	for _, spec := range specs {
		out = append(out, spec.SensitivePaths...)
	}
	return out
}

// ChangeClassPolicyActive reports whether an enabled approval policy of the
// tenant governs action by what its change touches: it matches a class, lists
// sensitive paths or requires someone other than the launcher. Without one a
// proposal is neither classified nor shown a class, and nothing about its
// approval changes.
func (a *EngineApprovals) ChangeClassPolicyActive(ctx context.Context, tenant model.TenantID, action string) (bool, error) {
	d, _, err := a.scope(tenant)
	if err != nil {
		return false, err
	}
	active := false
	err = d.View(ctx, func(sc store.Scope) error {
		pols, err := loadApprovalPolicies(ctx, sc)
		if err != nil {
			return err
		}
		specs, err := changeClassPolicies(pols, action)
		active = len(specs) > 0
		return err
	})
	return active, err
}

// ClassifyChange classifies the paths of a proposed change by the tenant's
// sensitive globs.
func (a *EngineApprovals) ClassifyChange(ctx context.Context, tenant model.TenantID, action string, paths []string) (ChangeClassification, error) {
	d, _, err := a.scope(tenant)
	if err != nil {
		return ChangeClassification{}, err
	}
	var out ChangeClassification
	err = d.View(ctx, func(sc store.Scope) error {
		pols, err := loadApprovalPolicies(ctx, sc)
		if err != nil {
			return err
		}
		specs, err := changeClassPolicies(pols, action)
		if err != nil {
			return err
		}
		out = classifyChange(paths, sensitiveGlobs(specs))
		return nil
	})
	return out, err
}
