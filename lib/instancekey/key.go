// Package instancekey stores a persistent per-installation secret used to
// protect browser session cookies. It is intentionally independent from
// administrator credentials so rotating a password does not invalidate the
// signing material or make sessions forgeable with a leaked config value.
package instancekey

import (
	"bytes"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	header  = "nps-instance-key-v1\n"
	keySize = 32
)

type Key struct{ secret [keySize]byte }

// LoadOrCreate reads an existing key or atomically creates it when absent.
// Malformed existing files are never silently replaced.
func LoadOrCreate(path string) (*Key, error) {
	key, err := read(path)
	if !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("create instance key directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".instance-key-*")
	if err != nil {
		return nil, fmt.Errorf("create instance key: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	secret := make([]byte, keySize)
	if _, err = rand.Read(secret); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("generate instance key: %w", err)
	}
	data := append([]byte(header), secret...)
	sum := sha256.Sum256(data)
	data = append(data, sum[:]...)
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write instance key: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("sync instance key: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return nil, fmt.Errorf("close instance key: %w", err)
	}
	if err = os.Chmod(tmpName, 0600); err != nil {
		return nil, fmt.Errorf("protect instance key: %w", err)
	}
	if err = os.Link(tmpName, path); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("publish instance key: %w", err)
	}
	return read(path)
}

func read(path string) (*Key, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(header)+keySize+sha256.Size) {
		return nil, fmt.Errorf("invalid instance key file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read instance key: %w", err)
	}
	if len(data) != len(header)+keySize+sha256.Size || !bytes.HasPrefix(data, []byte(header)) {
		return nil, fmt.Errorf("invalid instance key format: %s", path)
	}
	sum := sha256.Sum256(data[:len(header)+keySize])
	if !bytes.Equal(sum[:], data[len(header)+keySize:]) {
		return nil, fmt.Errorf("invalid instance key checksum: %s", path)
	}
	key := &Key{}
	copy(key.secret[:], data[len(header):len(header)+keySize])
	return key, nil
}

// DeriveKey derives purpose-specific material using HKDF-SHA256.
func (k *Key) DeriveKey(purpose string, length int) ([]byte, error) {
	if k == nil || purpose == "" || length <= 0 {
		return nil, fmt.Errorf("invalid instance key derivation parameters")
	}
	return hkdf.Key(sha256.New, k.secret[:], nil, purpose, length)
}
