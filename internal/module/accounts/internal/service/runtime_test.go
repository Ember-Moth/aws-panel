package service

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/herta-network/aws-panel/internal/module/accounts/contract"
)

func TestVerificationErrorsAreClassifiedWithoutRawSecrets(t *testing.T) {
	const secret = "sensitive-fixture-never-display"
	for _, tc := range []struct {
		err         error
		proxy       bool
		stage, code string
	}{
		{contract.ErrExpired, false, "configuration", "CREDENTIALS_EXPIRED"},
		{contract.ErrStale, true, "configuration", "CONNECTION_CHANGED"},
		{&smithy.GenericAPIError{Code: "AccessDenied", Message: secret}, true, "aws", "AccessDenied"},
		{&smithy.GenericAPIError{Code: strings.Repeat("x", 81), Message: secret}, true, "aws", "AWS_ERROR"},
		{x509.HostnameError{Host: secret}, true, "tls", "TLS_VALIDATION_FAILED"},
		{&tls.CertificateVerificationError{Err: errors.New(secret)}, true, "tls", "TLS_VALIDATION_FAILED"},
		{errors.New("username/password authentication failed: " + secret), true, "proxy", "PROXY_AUTH_FAILED"},
		{errors.New(secret), true, "proxy", "PROXY_CONNECTION_FAILED"},
		{errors.New(secret), false, "network", "CONNECTION_FAILED"},
	} {
		stage, code, message := verificationFailure(tc.err, tc.proxy)
		if stage != tc.stage || code != tc.code || strings.Contains(message, secret) {
			t.Errorf("unexpected public error: %s / %s / %s", stage, code, message)
		}
	}
}
