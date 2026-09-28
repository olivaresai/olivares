// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// RestoreFiles accepts only the original persistent filename and NetworkManager's
// currently observed runtime shadow. Neither value comes from helper request input.
func RestoreFiles(baseline, current string) (string, error) {
	if !persistentFilename(baseline) {
		return "", errors.New("network_persistent_baseline_refused")
	}
	if current == "" || current == baseline {
		return "", nil
	}
	if filepath.Clean(current) != current || filepath.Dir(current) != "/run/NetworkManager/system-connections" || !strings.HasSuffix(current, ".nmconnection") {
		return "", errors.New("network_runtime_shadow_refused")
	}
	return current, nil
}
func persistentFilename(name string) bool {
	return filepath.Clean(name) == name && filepath.Dir(name) == "/etc/NetworkManager/system-connections" && strings.HasSuffix(name, ".nmconnection")
}

// rootLedger is root's independent, append-only per-call evidence. The guard cannot
// replace it. Completion publication never rewrites its durable intent.
type rootLedger struct {
	dir      string
	uid, gid uint32
}

func (l rootLedger) write(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > 16384 {
		return ErrJournalFull
	}
	f, err := os.OpenFile(filepath.Join(l.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0640)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0640); err != nil {
		return err
	}
	if err := f.Chown(int(l.uid), int(l.gid)); err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	dir, err := os.Open(l.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func protectedJSON(name string, uid, gid uint32, mode os.FileMode, limit int64, out any) error {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != mode || st.Uid != uid || st.Gid != gid || st.Nlink != 1 {
		return errors.New("network_receipt_custody_refused")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(b)) > limit {
		return ErrJournalFull
	}
	if err := json.Unmarshal(b, out); err != nil {
		return errors.New("network_receipt_invalid")
	}
	return nil
}
func (l rootLedger) read(name string, v any) error {
	return protectedJSON(filepath.Join(l.dir, name), l.uid, l.gid, 0640, 16384, v)
}
func intentName(c CallRecord) string     { return fmt.Sprintf("%010d.intent.json", c.Serial) }
func completionName(c CallRecord) string { return fmt.Sprintf("%010d.completion.json", c.Serial) }
func sameCall(a, b CallRecord) bool {
	a.Settled = false
	a.Success = false
	a.Settlement = ""
	a.EffectKnownAt = 0
	a.EffectBootID = ""
	b.Settled = false
	b.Success = false
	b.Settlement = ""
	b.EffectKnownAt = 0
	b.EffectBootID = ""
	return a == b
}
func (l rootLedger) intent(c CallRecord) error {
	if c.Serial == 0 || c.Settled {
		return errors.New("network_intent_invalid")
	}
	return l.write(intentName(c), c)
}
func (l rootLedger) complete(c CallRecord) error {
	var intent CallRecord
	if err := l.read(intentName(c), &intent); err != nil {
		return err
	}
	if !sameCall(intent, c) || !c.Settled || c.Settlement != "correlated_reply" {
		return errors.New("network_completion_uncorrelated")
	}
	return l.write(completionName(c), c)
}
func (l rootLedger) calls() ([]CallRecord, error) {
	files, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	if len(files) > 8 {
		return nil, ErrJournalFull
	}
	var calls []CallRecord
	intents := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".intent.json") {
			intents[strings.TrimSuffix(file.Name(), ".intent.json")] = true
		}
	}
	for _, file := range files {
		name := file.Name()
		if strings.HasSuffix(name, ".completion.json") && !intents[strings.TrimSuffix(name, ".completion.json")] {
			return nil, errors.New("network_completion_without_intent")
		}
		if name != "begin.json" && name != "finished.json" && !strings.HasSuffix(name, ".intent.json") && !strings.HasSuffix(name, ".completion.json") {
			return nil, errors.New("network_restore_ledger_entry_refused")
		}
		if !strings.HasSuffix(file.Name(), ".intent.json") {
			continue
		}
		var c CallRecord
		if err := l.read(file.Name(), &c); err != nil {
			return nil, err
		}
		if file.Name() != intentName(c) || c.Settled {
			return nil, errors.New("network_intent_invalid")
		}
		var complete CallRecord
		if err := l.read(completionName(c), &complete); err == nil {
			if !sameCall(c, complete) || !complete.Settled || complete.Settlement != "correlated_reply" {
				return nil, errors.New("network_completion_uncorrelated")
			}
			c = complete
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		calls = append(calls, c)
	}
	return calls, nil
}
