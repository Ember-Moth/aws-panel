package token

import "testing"

func TestTokenAndCSRFAreBoundToSession(t *testing.T) {
	first, err := New()
	if err != nil {
		t.Fatal(err)
	}
	second, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !Valid(first) || Valid(Hash(first)) {
		t.Fatal("unexpected session token format")
	}
	csrf := CSRF(first, "session")
	if !VerifyCSRF(first, "session", csrf) || VerifyCSRF(second, "session", csrf) || VerifyCSRF(first, "login", csrf) || VerifyCSRF(first, "session", "") {
		t.Fatal("CSRF did not bind to the session and purpose")
	}
	if VerifyCSRF("invalid", "session", "") {
		t.Fatal("empty verifier accepted")
	}
}
