package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	MinLength   = 15
	MaxLength   = 128
	memory      = 64 * 1024
	iterations  = 3
	parallelism = 1
)

// Only two expensive computations may run at once in the application process.
// Waiting is cancellable; this bounds Argon2 memory under concurrent logins.
var slots = make(chan struct{}, 2)

func Validate(value string) error {
	count := utf8.RuneCountInString(value)
	if !utf8.ValidString(value) || count < MinLength || count > MaxLength {
		return errors.New("密码需要 15 至 128 个字符")
	}
	return nil
}

func Hash(ctx context.Context, value string) (string, error) {
	if err := Validate(value); err != nil {
		return "", err
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	key, err := derive(ctx, value, salt[:], memory, iterations, parallelism)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt[:]), base64.RawStdEncoding.EncodeToString(key)), nil
}

func Verify(ctx context.Context, encoded, value string) (bool, error) {
	if len(value) > 4*MaxLength || !utf8.ValidString(value) {
		return false, nil
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errors.New("invalid password hash")
	}
	var m, t uint32
	var p uint8
	n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p)
	if err != nil || n != 3 || fmt.Sprintf("m=%d,t=%d,p=%d", m, t, p) != parts[3] ||
		m < 19*1024 || m > 128*1024 || t < 1 || t > 5 || p < 1 || p > 4 {
		return false, errors.New("unsupported password hash parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false, errors.New("invalid password salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != 32 {
		return false, errors.New("invalid password verifier")
	}
	got, err := derive(ctx, value, salt, m, t, p)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// DummyCompare costs the same as a normal comparison for an unknown username.
func DummyCompare(ctx context.Context, value string) error {
	if len(value) > 4*MaxLength {
		return nil
	}
	_, err := derive(ctx, value, []byte("aws-panel-dummy!1"), memory, iterations, parallelism)
	return err
}

func derive(ctx context.Context, value string, salt []byte, m, t uint32, p uint8) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-wait.Done():
		return nil, wait.Err()
	}
	key := argon2.IDKey([]byte(value), salt, t, m, p, 32)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return key, nil
}
