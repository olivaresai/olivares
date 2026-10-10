// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	localCommunicationContentKeyFile = "communication-content-keyring.json"
	localCommunicationCursorKeyFile  = "communication-cursor-keyring.json"
)

// Local custody is created only inside a proven fresh SQLite initialization.
// A restart loads it; missing or invalid keys never authorize replacement.
func prepareLocalCommunicationCustody(ctx context.Context, dir string, create bool, cfg *communicationActivationConfig) (*communicationContentSealer, *sessions.CommunicationCursorTokenKeyring, communicationCursorKeyringStatus) {
	content := filepath.Join(dir, localCommunicationContentKeyFile)
	cursor := filepath.Join(dir, localCommunicationCursorKeyFile)
	present := create
	for _, path := range []string{content, cursor} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			present = true
		}
	}
	if !present {
		return nil, nil, communicationCursorKeyringStatus{}
	}
	cfg.Requested, cfg.ContentKeyringPath, cfg.CursorKeyringPath = true, content, cursor
	if create {
		for _, item := range []struct {
			path, setting string
			content       bool
		}{
			{content, envCommunicationContentKeyringFile, true}, {cursor, envCommunicationCursorKeyringFile, false},
		} {
			if err := createLocalCommunicationKeyring(item.path, item.content); err != nil {
				cfg.blockCustody(item.setting, err.Error())
			}
		}
	}
	sealer, err := loadCommunicationContentSealer(ctx, content, openCommunicationContentKeyringOperatorConfig)
	if err != nil {
		cfg.blockCustody(envCommunicationContentKeyringFile, err.Error())
	}
	keyring, status, err := loadCommunicationCursorKeyring(ctx, cursor, openCommunicationContentKeyringOperatorConfig, time.Now())
	if err != nil {
		cfg.blockCustody(envCommunicationCursorKeyringFile, err.Error())
	}
	return sealer, keyring, status
}

func createLocalCommunicationKeyring(path string, content bool) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		return err
	}
	defer wipeCommunicationContentBytes(root)
	key := base64.StdEncoding.EncodeToString(root)
	var doc any = communicationCursorKeyringDocument{Format: communicationCursorKeyringFormat, CurrentKID: "local-v1",
		Keys: []communicationCursorKeyringEntry{{KID: "local-v1", KeyBase64: key}}}
	if content {
		doc = map[string]any{
			"format":               communicationContentKeyringFormat,
			"current_seal_version": "local-v1", "current_digest_version": "local-v1",
			"keys": []map[string]string{{"version": "local-v1", "root_key_base64": key}},
		}
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	defer wipeCommunicationContentBytes(raw)
	f, err := os.CreateTemp(filepath.Dir(path), ".communication-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return syncDir(filepath.Dir(path))
}
