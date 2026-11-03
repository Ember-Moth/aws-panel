package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthenticatedBindingAndKeyVersions(t *testing.T) {
	ring, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{"secret-id", "connection", "connection-id", "aws_credentials"}
	value, err := ring.Seal(binding, []byte("secret-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := ring.Seal(binding, []byte("secret-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(value.Nonce, other.Nonce) || bytes.Contains(value.Ciphertext, []byte("secret-fixture")) {
		t.Fatal("ciphertext or nonce is not independent")
	}
	changed := binding
	changed.OwnerID = "another"
	if _, err := ring.Open(changed, value); err == nil {
		t.Fatal("ciphertext moved between owners")
	}
	value.Ciphertext[0] ^= 1
	if _, err := ring.Open(binding, value); err == nil {
		t.Fatal("tampering accepted")
	}
	value.Ciphertext[0] ^= 1
	wrong, _ := Generate()
	wrong.Keys[value.KeyID] = wrong.Keys[wrong.Active]
	if _, err := wrong.Open(binding, value); err == nil {
		t.Fatal("wrong key material accepted")
	}
	old := ring.Active
	if _, err := ring.Add(); err != nil {
		t.Fatal(err)
	}
	raw, err := ring.Open(binding, value)
	if err != nil || string(raw) != "secret-fixture" {
		t.Fatal("retained key cannot decrypt")
	}
	resealed, err := ring.Seal(binding, raw)
	if err != nil || resealed.KeyID == old {
		t.Fatal("active key not used")
	}
	delete(ring.Keys, old)
	if _, err := ring.Open(binding, value); err == nil {
		t.Fatal("removed key silently replaced")
	}
	if _, err := ring.Open(binding, resealed); err != nil {
		t.Fatal(err)
	}
}
func TestKeyringFileLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	ring, _ := Generate()
	if err := ring.Write(path, true); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("keyring file not private")
	}
	if err := ring.Write(path, true); err == nil {
		t.Fatal("initialization overwrote an existing keyring")
	}
	loaded, err := Load(path)
	if err != nil || loaded.Active != ring.Active {
		t.Fatal("keyring did not roundtrip")
	}
	_, _ = ring.Add()
	if err := ring.Write(path, false); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil || len(loaded.Keys) != 2 {
		t.Fatal("rotation dropped old keys")
	}
	if _, err := Load(""); err == nil {
		t.Fatal("empty keyring accepted")
	}
	for _, mode := range []os.FileMode{0666, 0644, 0640} {
		_ = os.Chmod(path, mode)
		if _, err := Load(path); err == nil {
			t.Fatal("shared keyring accepted")
		}
	}
}
