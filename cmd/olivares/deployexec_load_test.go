// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The LOUD half of the passthrough invariant, at the place an operator can still fix it.
//
// core/runtime/executor refuses a control-plane variable name silently: childEnv has no
// logger, it runs once per deploy, and failing there would turn an old config into a broken
// deploy in flight. That backstop is correct AND insufficient on its own — a silent skip
// leaves the operator believing the variable travels. So the boot-time config load calls the
// SAME exported predicate (executor.ValidatePassthrough) and refuses to start.
//
// Two properties decide whether this is worth anything, and they are separate cases below:
//   - the refusal must be LOUD and SPECIFIC: which backend block, which field, which NAME;
//   - and it must never echo the VALUE. Validation reads names out of the JSON; it never
//     looks the variable up in this process's environment, so there is nothing to leak —
//     TestDeployExecutorConfigRefusalNeverEchoesAValue is what keeps it that way.
//
// Refused input must also not survive as effective configuration: the loader returns the
// ZERO config, so even a caller that ignored the error cannot wire a backend from it.

// writeExecutorConfig writes an operator config file and points the environment at it. The
// case bodies quote a NAME through this package's existing quoteJSON, so a case can carry
// padding without the fixture deciding the answer for it.
func writeExecutorConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deploy-executor.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_DEPLOY_EXECUTOR_CONFIG", path)
}

func TestDeployExecutorConfigRefusesControlPlanePassthrough(t *testing.T) {
	for _, tc := range []struct {
		name    string
		block   string
		field   string
		refused string
	}{
		{name: "tofu", block: "tofu", field: "tofu.passthrough_env", refused: "OLIVARES_MASTER_KEY"},
		// Terraform is the same backend behind a binary flag, and it is a SECOND config block
		// with its own list. A loader that validated only the first would leave it open.
		{name: "terraform", block: "terraform", field: "terraform.passthrough_env", refused: "OLIVARES_ADMIN_TOKEN"},
		// The operator types a string into a JSON list, not a Go identifier: case and padding
		// are theirs, and the shared predicate trims and folds case before comparing.
		{name: "padded lower case", block: "tofu", field: "tofu.passthrough_env", refused: "  olivares_admin_token  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeExecutorConfig(t, `{"`+tc.block+`":{"credential_env":["VAULT_TOKEN"],`+
				`"passthrough_env":["TF_LOG",`+quoteJSON(tc.refused)+`]}}`)

			cfg, err := loadDeployExecutorConfig(discardLog())
			if err == nil {
				t.Fatal("a declared control-plane passthrough name started the process: the loud half is missing")
			}
			for _, want := range []string{"OLIVARES_DEPLOY_EXECUTOR_CONFIG", tc.field, tc.refused} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the diagnostic does not say %q, so the operator cannot find the entry: %v", want, err)
				}
			}
			// The legitimate neighbor must not be blamed: an operator told "passthrough_env is
			// wrong" without which entry would delete the one the deploy needs.
			if strings.Contains(err.Error(), "TF_LOG") {
				t.Errorf("the diagnostic blames a legitimate entry too: %v", err)
			}
			// Refused input is not effective input. The zero config is what the caller gets, so
			// even ignoring the error cannot wire a backend out of it.
			if !reflect.DeepEqual(cfg, deployExecutorConfig{}) {
				t.Errorf("a refused config was returned as effective: %#v", cfg)
			}
			if e := newDeployExecutor(cfg, nil, discardLog()); e != nil {
				t.Error("a backend was wired from the refused config")
			}
		})
	}
}

// Both blocks are reported in ONE pass. An operator who fixes tofu only to be stopped again
// by terraform learns the rule twice and trusts the diagnostic less.
func TestDeployExecutorConfigNamesEveryRefusedBlock(t *testing.T) {
	writeExecutorConfig(t, `{"tofu":{"passthrough_env":["OLIVARES_MASTER_KEY"]},`+
		`"terraform":{"passthrough_env":["OLIVARES_ADMIN_TOKEN"]}}`)

	_, err := loadDeployExecutorConfig(discardLog())
	if err == nil {
		t.Fatal("two refused blocks started the process")
	}
	for _, want := range []string{"tofu.passthrough_env", "OLIVARES_MASTER_KEY", "terraform.passthrough_env", "OLIVARES_ADMIN_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic stops before %q, so the second block is found only on the next boot: %v", want, err)
		}
	}
}

// The diagnostic carries NAMES. Validation never looks a name up in this process's
// environment, so a value cannot reach a log, a console or a support ticket — and this case
// is what says so, with the values actually present in the environment as they would be in
// production.
func TestDeployExecutorConfigRefusalNeverEchoesAValue(t *testing.T) {
	const controlPlaneValue = "sk-control-plane-must-never-cross"
	const thirdPartyValue = "AKIA-example-not-a-real-key"
	t.Setenv("OLIVARES_MASTER_KEY", controlPlaneValue)
	t.Setenv("AWS_SECRET_ACCESS_KEY", thirdPartyValue)

	writeExecutorConfig(t, `{"tofu":{"passthrough_env":["AWS_SECRET_ACCESS_KEY","OLIVARES_MASTER_KEY"]}}`)

	_, err := loadDeployExecutorConfig(discardLog())
	if err == nil {
		t.Fatal("the refused name started the process")
	}
	for _, value := range []string{controlPlaneValue, thirdPartyValue} {
		if strings.Contains(err.Error(), value) {
			t.Errorf("the diagnostic echoed a variable's VALUE: %v", err)
		}
	}
	// Printed on purpose, and only safe BECAUSE of the assertions above: a reviewer reading a
	// run can see the whole diagnostic and check the property by eye, instead of trusting a
	// substring test. If this ever carried a value the case would already have failed.
	t.Logf("diagnostic as an operator sees it: %v", err)
}

// And the refusal does not depend on the variable EXISTING here. The config declares what a
// child would receive; a name absent from this process today is present on the next host, so
// a loader that only refused what it could read would pass the list through on one machine
// and stop it on another.
func TestDeployExecutorConfigRefusesANameThisProcessDoesNotHave(t *testing.T) {
	const absent = "OLIVARES_NOT_SET_IN_THIS_PROCESS"
	if _, present := os.LookupEnv(absent); present {
		t.Skipf("%s is set in this process, so the case cannot prove what it is for", absent)
	}
	writeExecutorConfig(t, `{"tofu":{"passthrough_env":["`+absent+`"]}}`)

	if _, err := loadDeployExecutorConfig(discardLog()); err == nil {
		t.Fatal("an unset control-plane name was accepted: the check reads the environment, not the list")
	}
}

// The control that stops the loud half from over-reaching, mirroring the engine's. A config
// that hands ordinary third-party credentials to terraform is the ORDINARY one, and it must
// load UNCHANGED — a refusal here would read as extra security while breaking a real deploy,
// and no mutant of the refusal cases above would notice.
func TestDeployExecutorConfigKeepsLegitimateConfiguration(t *testing.T) {
	legit := []string{"HOME", "PATH", "TF_LOG", "AWS_SECRET_ACCESS_KEY", "GOOGLE_APPLICATION_CREDENTIALS"}
	writeExecutorConfig(t, `{"tofu":{"binary":"tofu","workdir_root":"/srv/tf",`+
		`"credential_env":["VAULT_TOKEN"],"passthrough_env":["HOME","PATH","TF_LOG",`+
		`"AWS_SECRET_ACCESS_KEY","GOOGLE_APPLICATION_CREDENTIALS"],"lock_timeout_seconds":30},`+
		`"terraform":{"binary":"terraform","passthrough_env":["HOME","PATH","TF_LOG",`+
		`"AWS_SECRET_ACCESS_KEY","GOOGLE_APPLICATION_CREDENTIALS"]},`+
		`"credential":{"kind":"file","path_template":"/run/creds/{{.Environment}}"}}`)

	cfg, err := loadDeployExecutorConfig(discardLog())
	if err != nil {
		t.Fatalf("a legitimate third-party passthrough list was refused: %v", err)
	}
	if cfg.Tofu == nil || cfg.Terraform == nil {
		t.Fatalf("the declarative blocks did not survive the load: %#v", cfg)
	}
	if !reflect.DeepEqual(cfg.Tofu.PassthroughEnv, legit) {
		t.Errorf("tofu passthrough_env = %q, want %q", cfg.Tofu.PassthroughEnv, legit)
	}
	if !reflect.DeepEqual(cfg.Terraform.PassthroughEnv, legit) {
		t.Errorf("terraform passthrough_env = %q, want %q", cfg.Terraform.PassthroughEnv, legit)
	}
	// The rest of the file is untouched by the check: it reads one field, and a validator that
	// quietly dropped a neighboring value would be the same defect in the other direction.
	if cfg.Tofu.WorkdirRoot != "/srv/tf" || cfg.Tofu.LockTimeoutSeconds != 30 || cfg.Terraform.Binary != "terraform" {
		t.Errorf("unrelated configuration changed across the load: %#v / %#v", cfg.Tofu, cfg.Terraform)
	}
	if cfg.Credential.Kind != "file" || cfg.Credential.PathTemplate != "/run/creds/{{.Environment}}" {
		t.Errorf("the credential block changed across the load: %#v", cfg.Credential)
	}
}

// Absent configuration stays optional, and that is a DIFFERENT answer from refused: no file
// means the module keeps its deny-closed unwired executor (an honest 503), while a refused
// file stops the boot. Collapsing the two would either break every unconfigured deployment
// or silently drop a refused list and act on the rest.
func TestDeployExecutorConfigAbsentAndBlankStayOptional(t *testing.T) {
	t.Run("no path", func(t *testing.T) {
		t.Setenv("OLIVARES_DEPLOY_EXECUTOR_CONFIG", "")
		cfg, err := loadDeployExecutorConfig(discardLog())
		if err != nil {
			t.Fatalf("an unset path must be optional: %v", err)
		}
		if !reflect.DeepEqual(cfg, deployExecutorConfig{}) {
			t.Errorf("an unset path produced configuration: %#v", cfg)
		}
	})
	t.Run("no declarative block", func(t *testing.T) {
		writeExecutorConfig(t, `{"docker":{"socket_path":"/var/run/docker.sock"}}`)
		cfg, err := loadDeployExecutorConfig(discardLog())
		if err != nil {
			t.Fatalf("a config with no tofu/terraform block must load: %v", err)
		}
		if cfg.Docker == nil || cfg.Tofu != nil || cfg.Terraform != nil {
			t.Errorf("the load invented or lost a block: %#v", cfg)
		}
	})
	t.Run("declared block with an empty list", func(t *testing.T) {
		writeExecutorConfig(t, `{"tofu":{"credential_env":["VAULT_TOKEN"],"passthrough_env":[]}}`)
		if _, err := loadDeployExecutorConfig(discardLog()); err != nil {
			t.Fatalf("an empty passthrough list must load: %v", err)
		}
	})
}
