package token

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

func New() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func Valid(value string) bool {
	if len(value) != 43 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == 32
}

func Hash(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

// CSRF binds the form token to the opaque, HttpOnly session verifier.
func CSRF(session, purpose string) string {
	if !Valid(session) {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(session))
	_, _ = mac.Write([]byte("aws-panel/csrf/v1/" + purpose))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func VerifyCSRF(session, purpose, provided string) bool {
	expected := CSRF(session, purpose)
	return expected != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}
