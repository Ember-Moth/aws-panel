// Package vault encrypts recoverable secrets with versioned, external keys.
package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var (
	ErrLocked  = errors.New("credential storage keyring is unavailable")
	ErrInvalid = errors.New("credential ciphertext or keyring is invalid")
	keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

type Keyring struct {
	Active string            `json:"active"`
	Keys   map[string]string `json:"keys"`
}
type Binding struct{ ID, OwnerType, OwnerID, Purpose string }
type Encrypted struct {
	Format     int    `json:"-"`
	KeyID      string `json:"-"`
	Nonce      []byte `json:"-"`
	Ciphertext []byte `json:"-"`
}

func Generate() (*Keyring, error) {
	ring := &Keyring{Keys: make(map[string]string)}
	if _, err := ring.Add(); err != nil {
		return nil, err
	}
	return ring, nil
}
func (r *Keyring) Add() (string, error) {
	if len(r.Keys) >= 128 {
		return "", ErrInvalid
	}
	if r.Keys == nil {
		r.Keys = make(map[string]string)
	}
	var key [32]byte
	var id [8]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	keyID := "key_" + hex.EncodeToString(id[:])
	r.Keys[keyID] = base64.StdEncoding.EncodeToString(key[:])
	r.Active = keyID
	return keyID, nil
}
func Load(path string) (*Keyring, error) {
	if path == "" {
		return nil, ErrLocked
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrLocked
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || info.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var ring Keyring
	if err := decoder.Decode(&ring); err != nil {
		return nil, ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, ErrInvalid
	}
	if len(ring.Keys) == 0 || len(ring.Keys) > 128 || !keyPattern.MatchString(ring.Active) {
		return nil, ErrInvalid
	}
	for id, value := range ring.Keys {
		key, err := base64.StdEncoding.DecodeString(value)
		if !keyPattern.MatchString(id) || err != nil || len(key) != 32 {
			return nil, ErrInvalid
		}
	}
	if _, ok := ring.Keys[ring.Active]; !ok {
		return nil, ErrInvalid
	}
	return &ring, nil
}

// Write replaces an existing keyring atomically. Rotation callers coordinate
// this with the database vault lock and retain old keys until re-encryption.
func (r *Keyring) Write(path string, create bool) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ErrInvalid
	}
	data = append(data, '\n')
	if create {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("cannot create keyring; choose a new private file")
		}
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return errors.New("cannot persist keyring")
		}
		return syncDirectory(filepath.Dir(path))
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".keyring-*")
	if err != nil {
		return errors.New("cannot prepare keyring update")
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot persist keyring update")
	}
	if err := os.Rename(name, path); err != nil {
		return errors.New("cannot replace keyring")
	}
	return syncDirectory(filepath.Dir(path))
}
func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open keyring directory")
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return errors.New("cannot sync keyring directory")
	}
	return nil
}

func (r *Keyring) aead(id string) (cipher.AEAD, error) {
	value, ok := r.Keys[id]
	if !ok {
		return nil, ErrInvalid
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, ErrInvalid
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrInvalid
	}
	return cipher.NewGCM(block)
}
func bindingData(binding Binding, keyID string) []byte {
	data, _ := json.Marshal(struct {
		Format  int
		KeyID   string
		Binding Binding
	}{1, keyID, binding})
	return data
}
func (r *Keyring) Seal(binding Binding, plaintext []byte) (Encrypted, error) {
	if binding.ID == "" || binding.OwnerType == "" || binding.OwnerID == "" || binding.Purpose == "" {
		return Encrypted{}, ErrInvalid
	}
	aead, err := r.aead(r.Active)
	if err != nil {
		return Encrypted{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Encrypted{}, ErrInvalid
	}
	return Encrypted{Format: 1, KeyID: r.Active, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plaintext, bindingData(binding, r.Active))}, nil
}
func (r *Keyring) Open(binding Binding, value Encrypted) ([]byte, error) {
	if value.Format != 1 {
		return nil, ErrInvalid
	}
	aead, err := r.aead(value.KeyID)
	if err != nil || len(value.Nonce) != aead.NonceSize() {
		return nil, ErrInvalid
	}
	plaintext, err := aead.Open(nil, value.Nonce, value.Ciphertext, bindingData(binding, value.KeyID))
	if err != nil {
		return nil, ErrInvalid
	}
	return bytes.Clone(plaintext), nil
}
