// Package unixpassword produces Linux shadow-compatible launch credentials.
package unixpassword

import (
	"context"
	"crypto/rand"
	"errors"
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/GehirnInc/crypt/sha512_crypt"
)

var ErrInvalid = errors.New("root 密码需要 15 至 128 个字符，且不能包含控制字符")
var hashes = make(chan struct{}, 2)
var pattern = regexp.MustCompile(`^\$6\$rounds=500000\$[./0-9A-Za-z]{16}\$[./0-9A-Za-z]{86}$`)

func ValidHash(value string) bool { return pattern.MatchString(value) }
func Validate(value string) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 15 || utf8.RuneCountInString(value) > 128 {
		return ErrInvalid
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ErrInvalid
		}
	}
	return nil
}
func Hash(ctx context.Context, value string) (string, error) {
	if err := Validate(value); err != nil {
		return "", err
	}
	select {
	case hashes <- struct{}{}:
		defer func() { <-hashes }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	const alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	for i, v := range salt {
		salt[i] = alphabet[int(v)%len(alphabet)]
	}
	raw := []byte(value)
	defer clear(raw)
	result, err := sha512_crypt.New().Generate(raw, []byte("$6$rounds=500000$"+string(salt[:])+"$"))
	if err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if !ValidHash(result) {
		return "", ErrInvalid
	}
	return result, nil
}
