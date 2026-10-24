// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// The store snapshot is the bulk of a bundle and holds the accounts, policies,
// audit ledger and session metadata, so it is sealed under the same KEK as the
// signing keys (keyenc.go). It is far too large for KeyCipher.Seal, which holds the
// whole message in memory, so it is sealed in chunks:
//
//	header   "OLVSNAP1" ‖ chunk size (uint32, big endian)
//	chunk*   nonce (12 random bytes) ‖ AES-256-GCM(up to chunk-size plaintext)
//
// Every chunk is authenticated with the additional data header ‖ chunk index
// (uint64) ‖ final flag (1 byte). The index makes a reordered, duplicated or
// dropped-from-the-middle chunk fail its tag; the final flag makes a truncated
// stream fail, because the last chunk read was not sealed as the last one. All
// chunks but the last are full, and a snapshot always has at least one chunk, so
// an empty one is a single empty final chunk.
const (
	// snapshotEncChunked is the value of StoreSnapshot.Encryption for this format.
	snapshotEncChunked = "aes-256-gcm-chunked-v1"
	// snapshotSealedSuffix turns the snapshot's file name into its tar member.
	snapshotSealedSuffix = ".enc"

	snapshotMagic = "OLVSNAP1"
	// snapshotChunkSize is the plaintext per chunk the writer uses: large enough
	// that the 28 bytes of nonce and tag are noise, small enough to stay out of memory.
	snapshotChunkSize = 1 << 20
	// snapshotMaxChunkSize bounds what a reader allocates for a chunk size taken from
	// an untrusted file before any tag has been checked.
	snapshotMaxChunkSize = 64 << 20
	snapshotHeaderLen    = len(snapshotMagic) + 4
)

func snapshotHeader(chunkSize int) []byte {
	h := make([]byte, snapshotHeaderLen)
	copy(h, snapshotMagic)
	binary.BigEndian.PutUint32(h[len(snapshotMagic):], uint32(chunkSize))
	return h
}

func snapshotAAD(header []byte, index uint64, final bool) []byte {
	aad := make([]byte, 0, len(header)+9)
	aad = append(aad, header...)
	aad = binary.BigEndian.AppendUint64(aad, index)
	if final {
		return append(aad, 1)
	}
	return append(aad, 0)
}

// sealStream reads src to its end and writes it to dst in the chunked format.
func (c *KeyCipher) sealStream(dst io.Writer, src io.Reader, chunkSize int) error {
	gcm, err := c.gcm()
	if err != nil {
		return err
	}
	header := snapshotHeader(chunkSize)
	if _, err := dst.Write(header); err != nil {
		return err
	}
	br := bufio.NewReader(src)
	plain := make([]byte, chunkSize)
	sealed := make([]byte, 0, chunkSize+gcm.Overhead())
	nonce := make([]byte, gcm.NonceSize())
	for index := uint64(0); ; index++ {
		n, err := io.ReadFull(br, plain)
		final := false
		switch err {
		case nil:
			// A full chunk is the last one only if nothing follows it.
			if _, perr := br.Peek(1); perr == io.EOF {
				final = true
			} else if perr != nil {
				return perr
			}
		case io.EOF, io.ErrUnexpectedEOF:
			final = true
		default:
			return err
		}
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return fmt.Errorf("dr: nonce: %w", err)
		}
		sealed = gcm.Seal(sealed[:0], nonce, plain[:n], snapshotAAD(header, index, final))
		if _, err := dst.Write(nonce); err != nil {
			return err
		}
		if _, err := dst.Write(sealed); err != nil {
			return err
		}
		if final {
			return nil
		}
	}
}

// openStream authenticates and decrypts a sealStream output into dst. It fails on
// a wrong KEK or on any altered, truncated, extended or reordered chunk; dst may
// already hold the earlier chunks then, so the caller discards it on error.
func (c *KeyCipher) openStream(dst io.Writer, src io.Reader) error {
	gcm, err := c.gcm()
	if err != nil {
		return err
	}
	br := bufio.NewReader(src)
	header := make([]byte, snapshotHeaderLen)
	if _, err := io.ReadFull(br, header); err != nil {
		return fmt.Errorf("dr: sealed snapshot header: %w", err)
	}
	if string(header[:len(snapshotMagic)]) != snapshotMagic {
		return errors.New("dr: sealed snapshot has an unknown header")
	}
	chunkSize := int(binary.BigEndian.Uint32(header[len(snapshotMagic):]))
	if chunkSize < 1 || chunkSize > snapshotMaxChunkSize {
		return fmt.Errorf("dr: sealed snapshot chunk size %d is out of range", chunkSize)
	}
	ns := gcm.NonceSize()
	frame := make([]byte, ns+chunkSize+gcm.Overhead())
	plain := make([]byte, 0, chunkSize)
	for index := uint64(0); ; index++ {
		n, err := io.ReadFull(br, frame)
		final := false
		switch err {
		case nil:
			if _, perr := br.Peek(1); perr == io.EOF {
				final = true
			} else if perr != nil {
				return perr
			}
		case io.ErrUnexpectedEOF:
			final = true
		case io.EOF:
			return errors.New("dr: sealed snapshot is truncated: it has no final chunk")
		default:
			return err
		}
		if n < ns+gcm.Overhead() {
			return fmt.Errorf("dr: sealed snapshot chunk %d is too short", index)
		}
		plain, err = gcm.Open(plain[:0], frame[:ns], frame[ns:n], snapshotAAD(header, index, final))
		if err != nil {
			return fmt.Errorf("dr: sealed snapshot chunk %d does not authenticate (wrong KEK, or the bundle was altered, truncated or reordered)", index)
		}
		if _, err := dst.Write(plain); err != nil {
			return err
		}
		if final {
			return nil
		}
	}
}

// sealSnapshotFile seals the plaintext snapshot at src into a new file beside it
// and returns that path; the caller removes it. The copy needs as much room as the
// snapshot itself, in the directory the caller staged the snapshot in; opening a
// sealed snapshot (VerifyBundleIntegrity) needs the same again in the work directory.
func sealSnapshotFile(c *KeyCipher, src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	out, err := os.CreateTemp(filepath.Dir(src), filepath.Base(src)+".sealed-*")
	if err != nil {
		return "", err
	}
	if err := c.sealStream(out, in, snapshotChunkSize); err != nil {
		_ = out.Close()
		_ = os.Remove(out.Name())
		return "", fmt.Errorf("dr: seal snapshot: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

// openSnapshotFile decrypts the sealed snapshot at sealed to dst and returns the
// size and SHA-256 of what it wrote. dst appears only when every chunk
// authenticated: it is written under a temporary name and renamed.
func openSnapshotFile(c *KeyCipher, sealed, dst string) (int64, string, error) {
	in, err := os.Open(sealed)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = in.Close() }()
	out, err := os.CreateTemp(filepath.Dir(dst), ".snapshot-open-*")
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	if err := c.openStream(io.MultiWriter(out, h), in); err != nil {
		_ = out.Close()
		_ = os.Remove(out.Name())
		return 0, "", err
	}
	size, err := out.Seek(0, io.SeekCurrent)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(out.Name())
		return 0, "", err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(out.Name())
		return 0, "", err
	}
	if err := os.Rename(out.Name(), dst); err != nil {
		_ = os.Remove(out.Name())
		return 0, "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}
