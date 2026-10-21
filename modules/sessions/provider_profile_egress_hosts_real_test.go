// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// dev#2154, real use: through the production runner, Landlock and the egress
// proxy, a record-bound Claude profile that declares github.com and
// api.github.com reaches those two hosts with curl and git and nothing else
// (a push's first request, GitHub's own authentication challenge, included),
// and a profile that declares none reaches neither. Both profiles are launched
// through the real launch path; only the child program is replaced by a shell
// and the bound provider by a local stand-in (no provider key is used).
// OLIVARES_TEST_REAL_EGRESS_HOSTS=1 runs it: it needs curl, git, Landlock,
// user namespaces and the internet.
func TestRealProfileEgressHostsReachOnlyTheDeclaredHosts(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_REAL_EGRESS_HOSTS") != "1" {
		t.Skip("OLIVARES_TEST_REAL_EGRESS_HOSTS is not set")
	}
	var tools []string
	for _, name := range []string{"curl", "git"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("%s is not installed", name)
		}
		tools = append(tools, filepath.Dir(path))
	}
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Fatal("this reproducer requires Landlock: " + s.Reason)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer provider.Close()

	dataDir := t.TempDir()
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(&countingCredentialSource{}),
		WithProviderSecretVault(newFakeVault()), WithClaudeHookPEP(dataDir, "/bin/true"), WithConfinement([]string{dataDir}, true))
	m.UseExecutionEnvironmentRef(testEnvRef)
	// A GLM key is an Anthropic-kind record at z.ai's endpoint. The launch's
	// bound endpoint is moved to the stand-in below, as in dev#1517's test.
	glm := anthropicInput("GLM")
	glm.BaseURL = "https://api.z.ai/api/anthropic"
	rec := mustCreateRecord(t, m, tenant, glm)
	configA, userA, configB, userB := twoHomes(t)
	declared := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configA, UserHome: userA,
		DisplayName: "with hosts", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref,
		SessionEgressHosts: githubHosts, MayUseSecretEnv: true})
	other := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configB, UserHome: userB,
		DisplayName: "without hosts", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})

	// One line per probe: the final HTTP status and the proxy's CONNECT status
	// (000 when no response came), or the tool's exit code and first stderr line.
	// A refusal is the proxy's own 403, not just any failure of the tool.
	const script = `
probe() { label=$1; shift; err=$(timeout 40 "$@" 2>&1 >/dev/null); rc=$?; echo "RESULT $label rc=$rc err=$(echo "$err" | head -n 1)"; }
http() { code=$(curl -s -o /dev/null --max-time 25 -w '%{http_code} %{http_connect}' "$2"); echo "RESULT $1 http=${code% *} connect=${code#* }"; }
http api.github.com https://api.github.com
http example.com https://example.com
http push-discovery "https://github.com/git/git.git/info/refs?service=git-receive-pack"
probe git-github git ls-remote https://github.com/git/git.git HEAD
probe git-gitlab git ls-remote https://gitlab.com/gitlab-org/gitlab-foss.git HEAD
`
	refused := []string{"http=000", "connect=403"}
	reached := func(status string) []string { return []string{"http=" + status, "connect=200"} }
	out, code := runBoundSession(t, m, tenant, runner, declared, provider.URL, script, tools...)
	if code != 0 {
		t.Fatalf("the declared profile's session failed (exit %d):\n%s", code, out)
	}
	// GitHub answers an unauthenticated API call with 200 or, once the host's
	// hourly allowance is spent, 403: either one is GitHub's own answer.
	for label, want := range map[string][]string{
		"api.github.com": {"connect=200"},
		"example.com":    refused,
		"push-discovery": reached("401"),
		"git-github":     {"rc=0 err="},
		"git-gitlab":     {"rc=128", "response 403"},
	} {
		expect(t, "declared profile", out, label, want)
	}
	if line := resultLine(out, "api.github.com"); strings.Contains(line, "http=000") {
		t.Errorf("declared profile, api.github.com: no answer from GitHub: %q", line)
	}
	t.Logf("declared profile %s:\n%s", declared.Ref, results(out))

	out, code = runBoundSession(t, m, tenant, runner, other, provider.URL, script, tools...)
	if code != 0 {
		t.Fatalf("the other profile's session failed (exit %d):\n%s", code, out)
	}
	for label, want := range map[string][]string{
		"api.github.com": refused,
		"example.com":    refused,
		"push-discovery": refused,
		"git-github":     {"rc=128", "response 403"},
		"git-gitlab":     {"rc=128", "response 403"},
	} {
		expect(t, "profile without hosts", out, label, want)
	}
	t.Logf("profile without hosts %s:\n%s", other.Ref, results(out))
}

var resultRE = regexp.MustCompile(`(?m)^RESULT .*$`)

func results(out string) string { return strings.Join(resultRE.FindAllString(out, -1), "\n") }

func expect(t *testing.T, who, out, label string, want []string) {
	t.Helper()
	line := resultLine(out, label)
	for _, part := range want {
		if !strings.Contains(line, part) {
			t.Errorf("%s, %s: %q does not contain %q\n%s", who, label, line, part, out)
			return
		}
	}
}

func resultLine(out, label string) string {
	for _, line := range resultRE.FindAllString(out, -1) {
		if strings.HasPrefix(line, "RESULT "+label+" ") {
			return line
		}
	}
	return ""
}
