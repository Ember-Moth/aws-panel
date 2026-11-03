package password

import (
	"context"
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	ctx := context.Background()
	value := "test-passphrase-for-identity"
	first, err := Hash(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Hash(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || strings.Contains(first, value) {
		t.Fatal("hash does not use independent salt")
	}
	ok, err := Verify(ctx, first, value)
	if err != nil || !ok {
		t.Fatal("correct password rejected")
	}
	ok, err = Verify(ctx, first, "wrong")
	if err != nil || ok {
		t.Fatal("wrong password accepted")
	}
}
func TestPolicyAndCorruptHash(t *testing.T) {
	if Validate("short") == nil || Validate(strings.Repeat("a", 129)) == nil {
		t.Fatal("password bounds not enforced")
	}
	if err := Validate(strings.Repeat("密", 15)); err != nil {
		t.Fatal("unicode length must count characters")
	}
	for _, encoded := range []string{"broken", "$argon2id$v=19$m=4294967295,t=3,p=1$a$b", "$argon2id$v=19$m=65536,t=3,p=0$a$b"} {
		if ok, err := Verify(context.Background(), encoded, "test-password"); err == nil || ok {
			t.Fatal("corrupt or unbounded hash accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Hash(ctx, "test-passphrase-for-identity"); err == nil {
		t.Fatal("cancelled hash completed")
	}
}
