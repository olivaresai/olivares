// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/secure"
)

func writeCommunicationContentKeyringFile(
	t *testing.T,
	directory string,
	name string,
	raw []byte,
	mode os.FileMode,
) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write keyring: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod keyring: %v", err)
	}
	return path
}

func communicationContentCustodyTestRaw(t *testing.T) []byte {
	t.Helper()
	return communicationContentTestKeyring(t, "seal-v1", "digest-v1",
		communicationContentTestRoot{"seal-v1", communicationContentTestRootBytes(0x81)},
		communicationContentTestRoot{"digest-v1", communicationContentTestRootBytes(0x82)},
	)
}

func TestCommunicationContentSealerLoaderAbsentIsInert(t *testing.T) {
	directory := t.TempDir()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	sealer, err := loadCommunicationContentSealer(canceled, "", func(context.Context, []byte) ([]byte, error) {
		called = true
		return nil, errors.New("must not be called")
	})
	if err != nil || sealer != nil {
		t.Fatalf("absent config = (%v, %v), want (nil, nil)", sealer, err)
	}
	if called {
		t.Fatal("absent config invoked custody")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("absent config minted files: %v, %v", entries, err)
	}
}

func TestCommunicationContentSealerLoaderPlaintextNeverInvokesCustody(t *testing.T) {
	raw := communicationContentCustodyTestRaw(t)
	path := writeCommunicationContentKeyringFile(t, t.TempDir(), "keyring.json", raw, 0o400)
	var calls atomic.Int32
	sealer, err := loadCommunicationContentSealer(
		context.Background(), path, func(context.Context, []byte) ([]byte, error) {
			calls.Add(1)
			return nil, errors.New("plaintext must not invoke custody")
		},
	)
	if err != nil || sealer == nil {
		t.Fatalf("load plaintext = (%v, %v)", sealer, err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("plaintext custody calls = %d", got)
	}
	ready, err := sealer.CommunicationContentSealerReady(context.Background())
	if err != nil || !ready {
		t.Fatalf("loaded readiness = %v, %v", ready, err)
	}
}

func TestCommunicationContentSealerLoaderPermissionWhitelist(t *testing.T) {
	raw := communicationContentCustodyTestRaw(t)
	for _, mode := range []os.FileMode{0o400, 0o440, 0o600, 0o640} {
		t.Run(mode.String(), func(t *testing.T) {
			path := writeCommunicationContentKeyringFile(
				t, t.TempDir(), "keyring.json", raw, mode,
			)
			if _, err := loadCommunicationContentSealer(context.Background(), path, nil); err != nil {
				t.Fatalf("mode %04o rejected: %v", mode, err)
			}
		})
	}
	for _, mode := range []os.FileMode{
		0o000, 0o040, 0o240, 0o444, 0o500, 0o644, 0o660, 0o700,
	} {
		t.Run("reject-"+mode.String(), func(t *testing.T) {
			path := writeCommunicationContentKeyringFile(
				t, t.TempDir(), "keyring.json", raw, mode,
			)
			if _, err := loadCommunicationContentSealer(context.Background(), path, nil); err == nil {
				t.Fatalf("mode %04o accepted", mode)
			}
		})
	}

	for _, special := range []os.FileMode{os.ModeSetuid, os.ModeSetgid, os.ModeSticky} {
		t.Run("special-"+special.String(), func(t *testing.T) {
			path := writeCommunicationContentKeyringFile(
				t, t.TempDir(), "keyring.json", raw, 0o400|special,
			)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&special == 0 {
				t.Fatalf("fixture did not retain special mode %v", special)
			}
			if _, err := loadCommunicationContentSealer(context.Background(), path, nil); err == nil {
				t.Fatalf("special mode %v accepted", special)
			}
		})
	}
}

func TestCommunicationContentSealerLoaderFstatsAndReadsSameDescriptor(t *testing.T) {
	source, err := os.ReadFile("communicationsealercustody.go") //nolint:gosec // structural mutant guard
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]byte{
		[]byte("info, err := f.Stat()"),
		[]byte("if !info.Mode().IsRegular()"),
		[]byte("io.LimitReader(f, communicationContentMaxKeyring+1)"),
	} {
		if !bytes.Contains(source, required) {
			t.Fatalf("same-descriptor custody boundary lost %q", required)
		}
	}
	if bytes.Contains(source, []byte("os.ReadFile(path)")) ||
		bytes.Contains(source, []byte("os.Stat(path)")) {
		t.Fatal("keyring loader reopens or restats the pathname after descriptor acquisition")
	}
}

func TestCommunicationContentSealerLoaderDescriptorAndSizeBounds(t *testing.T) {
	directory := t.TempDir()
	if _, err := loadCommunicationContentSealer(context.Background(), directory, nil); err == nil {
		t.Fatal("directory accepted as a regular keyring")
	}
	missing := filepath.Join(directory, "missing.json")
	if _, err := loadCommunicationContentSealer(context.Background(), missing, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path error = %v", err)
	}
	empty := writeCommunicationContentKeyringFile(t, directory, "empty.json", nil, 0o400)
	if _, err := loadCommunicationContentSealer(context.Background(), empty, nil); err == nil {
		t.Fatal("empty keyring accepted")
	}
	atLimit := writeCommunicationContentKeyringFile(
		t, directory, "at-limit.json", bytes.Repeat([]byte{' '}, communicationContentMaxKeyring), 0o400,
	)
	if raw, err := readCommunicationContentKeyring(atLimit); err != nil || len(raw) != communicationContentMaxKeyring {
		t.Fatalf("at-limit descriptor read = %d, %v", len(raw), err)
	}
	overLimit := writeCommunicationContentKeyringFile(
		t, directory, "over-limit.json", bytes.Repeat([]byte{' '}, communicationContentMaxKeyring+1), 0o400,
	)
	if _, err := readCommunicationContentKeyring(overLimit); err == nil {
		t.Fatal("over-limit descriptor read succeeded")
	}
	fifo := filepath.Join(directory, "keyring.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := readCommunicationContentKeyring(fifo)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("named FIFO accepted as a keyring")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("named FIFO blocked before descriptor fstat")
	}
}

func TestCommunicationContentSealerLoaderUnwrapsOnceAndWipesCallbackMemory(t *testing.T) {
	raw := communicationContentCustodyTestRaw(t)
	sealedMarker := []byte(`{"olivares_sealed":1,"ciphertext":"opaque"}`)
	path := writeCommunicationContentKeyringFile(t, t.TempDir(), "keyring.sealed", sealedMarker, 0o440)
	retained := append([]byte(nil), raw...)
	var calls atomic.Int32
	sealer, err := loadCommunicationContentSealer(
		context.Background(), path, func(context.Context, []byte) ([]byte, error) {
			calls.Add(1)
			return retained, nil
		},
	)
	if err != nil || sealer == nil {
		t.Fatalf("sealed load = (%v, %v)", sealer, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("unwrap calls = %d, want 1", got)
	}
	if !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("loader retained callback-owned plaintext instead of wiping it")
	}

	aad := communicationContentTestAAD()
	plaintext := []byte(`{"local":"hot-path"}`)
	ciphertext, version, err := sealer.Seal(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sealer.Open(context.Background(), aad, ciphertext, version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sealer.Digest(context.Background(), aad, plaintext); err != nil {
		t.Fatal(err)
	}
	if ready, err := sealer.CommunicationContentSealerReady(context.Background()); err != nil || !ready {
		t.Fatalf("ready = %v, %v", ready, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("hot path repeated custody unwrap: %d", got)
	}
}

func TestCommunicationContentSealerLoaderWipesUnwrapOnEveryFailure(t *testing.T) {
	sealedMarker := []byte(`{"olivares_sealed":1}`)
	path := writeCommunicationContentKeyringFile(t, t.TempDir(), "keyring.sealed", sealedMarker, 0o600)
	cause := errors.New("custody refused")
	retained := []byte("sensitive partial plaintext")
	if _, err := loadCommunicationContentSealer(
		context.Background(), path, func(context.Context, []byte) ([]byte, error) {
			return retained, cause
		},
	); !errors.Is(err, cause) || !errors.Is(err, errCommunicationContentCustody) {
		t.Fatalf("unwrap causal error = %v", err)
	}
	if !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("errored unwrap plaintext was not wiped")
	}

	ctx, cancel := context.WithCancel(context.Background())
	retained = communicationContentCustodyTestRaw(t)
	if _, err := loadCommunicationContentSealer(
		ctx, path, func(context.Context, []byte) ([]byte, error) {
			cancel()
			return retained, nil
		},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel-after-unwrap error = %v", err)
	}
	if !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("canceled unwrap plaintext was not wiped")
	}

	if _, err := loadCommunicationContentSealer(context.Background(), path, nil); !errors.Is(err, errCommunicationContentCustody) {
		t.Fatalf("sealed config without unwrap error = %v", err)
	}
}

type communicationContentBlockingRandom struct {
	entered chan struct{}
	release chan struct{}
	once    atomic.Bool
}

func (r *communicationContentBlockingRandom) Read(dst []byte) (int, error) {
	if r.once.CompareAndSwap(false, true) {
		close(r.entered)
	}
	<-r.release
	for index := range dst {
		dst[index] = byte(index + 1)
	}
	return len(dst), nil
}

func TestCommunicationContentSealerConstructorObservesCancellationDuringSelfTest(t *testing.T) {
	raw := communicationContentCustodyTestRaw(t)
	ctx, cancel := context.WithCancel(context.Background())
	random := &communicationContentBlockingRandom{
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	result := make(chan error, 1)
	go func() {
		_, err := newCommunicationContentSealerContextWithRandom(ctx, raw, random)
		result <- err
	}()
	select {
	case <-random.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("constructor did not enter its RNG self-test")
	}
	cancel()
	close(random.release)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("constructor cancellation error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("constructor did not leave the canceled self-test")
	}
}

func TestCommunicationContentSealerOperatorCustodyBootUnwrapOnly(t *testing.T) {
	fakeKMS := startFakeKEKServer(t)
	raw := communicationContentCustodyTestRaw(t)
	cfg, err := loadKeyWrapConfig()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := cfg.wrapper()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := secure.Seal(
		context.Background(), wrapper, secure.PurposeOperatorConfig, raw,
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "communication-keyring.sealed")
	if err := secure.WriteSealedFile(path, envelope); err != nil {
		t.Fatal(err)
	}
	sealer, err := loadCommunicationContentSealer(
		context.Background(), path, openCommunicationContentKeyringOperatorConfig,
	)
	if err != nil {
		t.Fatalf("load sealed keyring: %v", err)
	}

	// Revocation after boot cannot affect local content operations. The next
	// boot unwrap fails, which is the existing operator-config custody model.
	fakeKMS.revoked = true
	aad := communicationContentTestAAD()
	plaintext := []byte(`{"cmek":"local-after-boot"}`)
	ciphertext, version, err := sealer.Seal(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatalf("Seal after KEK revoke: %v", err)
	}
	if opened, err := sealer.Open(context.Background(), aad, ciphertext, version); err != nil ||
		!bytes.Equal(opened, plaintext) {
		t.Fatalf("Open after KEK revoke = %q, %v", opened, err)
	}
	if _, err := loadCommunicationContentSealer(
		context.Background(), path, openCommunicationContentKeyringOperatorConfig,
	); !errors.Is(err, errCommunicationContentCustody) {
		t.Fatalf("next boot after KEK revoke error = %v", err)
	}
}

func TestCommunicationContentSealerOperatorCustodyRejectsPurposeAndProvider(t *testing.T) {
	for _, mutate := range []struct {
		name    string
		purpose string
		change  func(*secure.SealedEnvelope)
	}{
		{name: "wrong-purpose", purpose: secure.PurposePolicySigningKey},
		{
			name: "wrong-provider", purpose: secure.PurposeOperatorConfig,
			change: func(envelope *secure.SealedEnvelope) { envelope.Provider = "gcp-kms" },
		},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			startFakeKEKServer(t)
			cfg, err := loadKeyWrapConfig()
			if err != nil {
				t.Fatal(err)
			}
			wrapper, err := cfg.wrapper()
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := secure.Seal(
				context.Background(), wrapper, mutate.purpose, communicationContentCustodyTestRaw(t),
			)
			if err != nil {
				t.Fatal(err)
			}
			if mutate.change != nil {
				mutate.change(envelope)
			}
			path := filepath.Join(t.TempDir(), "keyring.sealed")
			if err := secure.WriteSealedFile(path, envelope); err != nil {
				t.Fatal(err)
			}
			if _, err := loadCommunicationContentSealer(
				context.Background(), path, openCommunicationContentKeyringOperatorConfig,
			); !errors.Is(err, errCommunicationContentCustody) {
				t.Fatalf("custody mismatch error = %v", err)
			}
		})
	}
}

func TestCommunicationContentSealerLoaderDoesNotHideContextOrReaderErrors(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "not-read")
	if _, err := loadCommunicationContentSealer(canceled, path, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-read cancellation error = %v", err)
	}

	cause := errors.New("random source failed")
	reader := readerFunc(func([]byte) (int, error) { return 0, cause })
	if _, err := newCommunicationContentSealerContextWithRandom(
		context.Background(), communicationContentCustodyTestRaw(t), reader,
	); !errors.Is(err, cause) {
		t.Fatalf("self-test random cause = %v", err)
	}
}

type readerFunc func([]byte) (int, error)

func (fn readerFunc) Read(value []byte) (int, error) { return fn(value) }

var _ io.Reader = readerFunc(nil)
