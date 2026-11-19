package service

import (
	"testing"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
)

func TestAccountFromAccessKey(t *testing.T) {
	cases := []struct {
		key, account string
		ok           bool
	}{
		{"ASIAY34FZKBOKMUTVV7A", "609629065308", true},
		{" ASIAY34FZKBOKMUTVV7A ", "609629065308", true},
		{"AKIAFIXTUREPRIMARY123", "", false},
		{"AROAY34FZKBOKMUTVV7A", "", false},
		{"AKIA0000000000000000", "", false},
		{"AKIA!!!!!!!!!!!!!!!!", "", false},
	}
	for _, test := range cases {
		account, ok := accountFromAccessKey(test.key)
		if account != test.account || ok != test.ok {
			t.Errorf("accountFromAccessKey(%q) = %q, %v", test.key, account, ok)
		}
	}
}

func TestPrepareInitialConnection(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	req := contract.AccountRequest{Name: "Team", Partition: "aws-cn", Connection: &contract.ConnectionRequest{AuthType: "temporary",
		Credentials: &contract.Credentials{AccessKeyID: "ASIAY34FZKBOKMUTVV7A", SecretAccessKey: "fixture-secret-access-key", SessionToken: "token", ExpiresAt: &expires}}}
	if err := prepareInitialConnection(&req); err != nil {
		t.Fatal(err)
	}
	if req.AWSAccountID != "609629065308" || req.Connection.Region != "cn-north-1" || req.Connection.RouteMode != "inherit" || req.Connection.Name != "默认连接" {
		t.Fatalf("unexpected defaults: %+v %+v", req, *req.Connection)
	}

	explicit := contract.AccountRequest{Name: "Team", Partition: "aws", AWSAccountID: "123456789012", Connection: &contract.ConnectionRequest{AuthType: "access_key", Region: "eu-west-1",
		Credentials: &contract.Credentials{AccessKeyID: "AKIAFIXTUREPRIMARY123", SecretAccessKey: "fixture-secret-access-key"}}}
	if err := prepareInitialConnection(&explicit); err != nil || explicit.AWSAccountID != "123456789012" || explicit.Connection.Region != "eu-west-1" {
		t.Fatalf("explicit account ID was not kept: %v %+v", err, explicit)
	}

	undecodable := explicit
	undecodable.AWSAccountID = ""
	undecodable.Connection = &contract.ConnectionRequest{AuthType: "access_key", Credentials: &contract.Credentials{AccessKeyID: "AKIAFIXTUREPRIMARY123", SecretAccessKey: "fixture-secret-access-key"}}
	if err := prepareInitialConnection(&undecodable); err == nil {
		t.Fatal("an account ID was invented for an undecodable key")
	}

	role := contract.AccountRequest{Name: "Team", Partition: "aws", AWSAccountID: "123456789012", Connection: &contract.ConnectionRequest{AuthType: "assume_role", Credentials: &contract.Credentials{}}}
	if err := prepareInitialConnection(&role); err == nil {
		t.Fatal("AssumeRole accepted as an initial connection")
	}
}
