package unixpassword

import (
	"context"
	"github.com/GehirnInc/crypt/sha512_crypt"
	"os/exec"
	"strings"
	"testing"
)

func TestLaunchHashSaltAndPasswordBounds(t *testing.T) {
	password := "Root fixture ' $ ` \\ ; 2026!"
	first, err := Hash(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Hash(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !ValidHash(first) || strings.Contains(first, password) {
		t.Fatal("launch password was not independently salted")
	}
	if sha512_crypt.New().Verify(first, []byte(password)) != nil {
		t.Fatal("hash does not verify the supplied password")
	}
	if sha512_crypt.New().Verify(first, []byte(password+"wrong")) == nil {
		t.Fatal("wrong password accepted")
	}
	for _, value := range []string{"short", strings.Repeat("x", 129), "long-enough-password\n", "long-enough-\x00-password"} {
		if Validate(value) == nil {
			t.Fatal("invalid password accepted")
		}
	}
	if ValidHash(first+"'; injected") || ValidHash(strings.Replace(first, "rounds=500000", "rounds=1000", 1)) {
		t.Fatal("unsafe or weakened hash accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Hash(ctx, password); err == nil {
		t.Fatal("cancelled hash request completed")
	}
}

func TestLaunchHashMatchesOpenSSL(t *testing.T) {
	path, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("OpenSSL unavailable")
	}
	const password = "Cross-implementation root password 2026!"
	hash, err := Hash(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(hash, "$")
	command := exec.Command(path, "passwd", "-6", "-salt", parts[2]+"$"+parts[3], "-stdin")
	command.Stdin = strings.NewReader(password + "\n")
	output, err := command.Output()
	if err != nil {
		t.Skip("OpenSSL SHA512-crypt unavailable")
	}
	if strings.TrimSpace(string(output)) != hash {
		t.Fatal("generated Linux password hash differs from OpenSSL")
	}
}
