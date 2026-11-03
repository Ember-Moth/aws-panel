package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/auth/vault"
	"github.com/Ember-Moth/aws-panel/internal/module/accounts"
	ac "github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/identity"
	ic "github.com/Ember-Moth/aws-panel/internal/module/identity/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/platform"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
)

func TestAccountsVaultRoutingAndAuthorization(t *testing.T) {
	db, pool, owner := identityDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "keys.json")
	ring, _ := vault.Generate()
	if err := ring.Write(path, true); err != nil {
		t.Fatal(err)
	}
	store := accounts.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender)
	catalog := accounts.NewCatalog(store)
	members := identity.New(identity.Deps{Store: identity.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender), Limiter: allowLogin{}, SessionTTL: time.Hour, AccountExists: catalog.Exists, AccountLabel: catalog.Label})
	meta := auth.RequestMeta{RequestID: "accounts-test", ClientIP: "127.0.0.1"}
	admin, err := members.Bootstrap(ctx, ic.CreateMemberRequest{Username: "admin", DisplayName: "Admin", Password: "accounts-admin-fixture"}, meta)
	if err != nil {
		t.Fatal(err)
	}
	operator, err := members.CreateMember(ctx, admin.ID, ic.CreateMemberRequest{Username: "operator", DisplayName: "Operator", Role: ic.RoleOperator, Password: "accounts-operator-fixture"}, meta)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := members.CreateMember(ctx, admin.ID, ic.CreateMemberRequest{Username: "viewer", DisplayName: "Viewer", Role: ic.RoleViewer, Password: "accounts-viewer-fixture"}, meta)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newSTSServer(t)
	proxyA := newSOCKSProxy(t, "proxy-a", "alpha", "proxy-password-alpha", fixture)
	proxyB := newSOCKSProxy(t, "proxy-b", "beta", "proxy-password-beta", fixture)
	service := accounts.New(accounts.Deps{Store: store, Access: members, KeyringFile: path, STS: fixture})
	t.Cleanup(service.Close)
	accountA, err := service.SaveAccount(ctx, admin.ID, "", ac.AccountRequest{Name: "Primary", Partition: "aws", AWSAccountID: "111111111111"}, meta)
	if err != nil {
		t.Fatal(err)
	}
	accountB, err := service.SaveAccount(ctx, admin.ID, "", ac.AccountRequest{Name: "Role target", Partition: "aws", AWSAccountID: "222222222222"}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveAccount(ctx, admin.ID, "", ac.AccountRequest{Name: "Duplicate", Partition: "aws", AWSAccountID: "111111111111"}, meta); !errors.Is(err, ac.ErrConflict) {
		t.Fatal("duplicate AWS identity accepted")
	}
	p1, err := service.SaveProxy(ctx, admin.ID, "", ac.ProxyRequest{Name: "A", Host: proxyA.host, Port: proxyA.port, Auth: &ac.ProxyAuth{Username: "alpha", Password: "proxy-password-alpha"}}, meta)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := service.SaveProxy(ctx, admin.ID, "", ac.ProxyRequest{Name: "B", Host: proxyB.host, Port: proxyB.port, Auth: &ac.ProxyAuth{Username: "beta", Password: "proxy-password-beta"}}, meta)
	if err != nil {
		t.Fatal(err)
	}
	keyA := ac.Credentials{AccessKeyID: testKeyA, SecretAccessKey: testSecretA}
	connectionA, err := service.SaveConnection(ctx, admin.ID, accountA.ID, "", ac.ConnectionRequest{Name: "Source", AuthType: "access_key", Region: "us-east-1", RouteMode: "socks5", ProxyID: &p1.ID, Credentials: &keyA}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfigFor(ctx, admin.ID, connectionA.ID, "", false); !errors.Is(err, ac.ErrUnverified) {
		t.Fatal("unverified connection can execute resource calls")
	}
	verify := func(id string) {
		t.Helper()
		result, err := service.Verify(ctx, admin.ID, id, meta)
		if err != nil || !result.OK {
			t.Fatalf("verification failed: %v, %s", err, result.Code)
		}
	}
	verify(connectionA.ID)
	connectionB, err := service.SaveConnection(ctx, admin.ID, accountB.ID, "", ac.ConnectionRequest{Name: "Role", AuthType: "assume_role", Region: "us-east-1", RouteMode: "socks5", ProxyID: &p2.ID, SourceConnectionID: &connectionA.ID,
		RoleARN: "arn:aws:iam::222222222222:role/Test", Credentials: &ac.Credentials{ExternalID: testExternalID}}, meta)
	if err != nil {
		t.Fatal(err)
	}
	verify(connectionB.ID)
	impact, err := service.Proxy(ctx, admin.ID, p1.ID)
	if err != nil || len(impact.Affected) != 2 {
		t.Fatal("proxy impact omitted dependent role connections")
	}
	var concurrent sync.WaitGroup
	for i := range 8 {
		concurrent.Add(1)
		go func() {
			defer concurrent.Done()
			id, expected := connectionA.ID, accountA.AWSAccountID
			if i%2 == 1 {
				id, expected = connectionB.ID, accountB.AWSAccountID
			}
			cfg, err := service.ConfigFor(ctx, admin.ID, id, "", false)
			if err != nil {
				t.Error(err)
				return
			}
			identity, err := fixture.Identity(ctx, cfg)
			if err != nil || identity.AccountID != expected {
				t.Error("concurrent account identity mismatch")
			}
		}()
	}
	concurrent.Wait()
	fixture.assertRoutes(t)
	snapshot, err := service.Inspect(ctx, admin.ID, connectionB.ID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	leaseLost := errors.New("test task lease expired")
	var denyLease atomic.Bool
	bound, err := service.ConfigForBound(ctx, admin.ID, connectionB.ID, "", false, snapshot.Fingerprint, func(context.Context) error {
		if denyLease.Load() {
			return leaseLost
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Identity(ctx, bound); err != nil {
		t.Fatal(err)
	}
	denyLease.Store(true)
	if _, err := fixture.Identity(ctx, bound); !errors.Is(err, leaseLost) {
		t.Fatal("bound client ignored the task lease guard")
	}
	beforeRefresh := fixture.assumptions.Load()
	time.Sleep(1200 * time.Millisecond)
	if _, err := bound.Credentials.Retrieve(ctx); !errors.Is(err, leaseLost) {
		t.Fatal("STS refresh ignored the task lease guard")
	}
	if fixture.assumptions.Load() != beforeRefresh {
		t.Fatal("expired task lease issued another STS call")
	}
	cfg, err := service.ConfigFor(ctx, admin.ID, connectionB.ID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	before := fixture.assumptions.Load()
	time.Sleep(1200 * time.Millisecond)
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		t.Fatal(err)
	}
	if fixture.assumptions.Load() <= before {
		t.Fatal("AssumeRole credentials did not refresh before expiry")
	}
	if err := members.SetAccountGrant(ctx, admin.ID, operator.ID, ic.GrantRequest{AccountID: accountB.ID, Access: "operate"}, meta); err != nil {
		t.Fatal(err)
	}
	if err := members.SetAccountGrant(ctx, admin.ID, viewer.ID, ic.GrantRequest{AccountID: accountB.ID, Access: "operate"}, meta); err != nil {
		t.Fatal(err)
	}
	visible, err := service.ListAccounts(ctx, operator.ID)
	if err != nil || len(visible) != 1 || visible[0].ID != accountB.ID {
		t.Fatal("account scope not applied")
	}
	if _, err := service.AccountDetail(ctx, operator.ID, accountA.ID); !errors.Is(err, ac.ErrForbidden) {
		t.Fatal("source account metadata escaped target grant")
	}
	if _, err := service.ConfigFor(ctx, viewer.ID, connectionB.ID, "", true); !errors.Is(err, ac.ErrForbidden) {
		t.Fatal("viewer gained write permission from account grant")
	}
	operatorConfig, err := service.ConfigFor(ctx, operator.ID, connectionB.ID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := members.SetAccountGrant(ctx, admin.ID, operator.ID, ic.GrantRequest{AccountID: accountB.ID, Access: "none"}, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Identity(ctx, operatorConfig); !errors.Is(err, ac.ErrForbidden) {
		t.Fatal("cached client ignored revoked grant")
	}
	active := true
	_, err = service.SaveConnection(ctx, admin.ID, accountA.ID, connectionA.ID, ac.ConnectionRequest{Name: "Cycle", AuthType: "assume_role", Region: "us-east-1", RouteMode: "direct", SourceConnectionID: &connectionB.ID, RoleARN: "arn:aws:iam::111111111111:role/Test", Credentials: &ac.Credentials{}, Active: &active, Version: connectionA.Version}, meta)
	if !errors.Is(err, ac.ErrInvalid) {
		t.Fatal("role cycle accepted")
	}
	sourceRow, err := store.Connection(ctx, connectionA.ID)
	if err != nil || sourceRow.AuthType != "access_key" {
		t.Fatal("cycle failure changed stored credentials")
	}
	originalConfig, err := service.ConfigFor(ctx, admin.ID, connectionA.ID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	p1, err = service.SaveProxy(ctx, admin.ID, p1.ID, ac.ProxyRequest{Name: p1.Name, Host: p1.Host, Port: p1.Port, Active: &disabled, Version: p1.Version}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Identity(ctx, originalConfig); !errors.Is(err, ac.ErrDisabled) {
		t.Fatal("old client continued after proxy disable")
	}
	p1, err = service.SaveProxy(ctx, admin.ID, p1.ID, ac.ProxyRequest{Name: p1.Name, Host: p1.Host, Port: p1.Port, Active: &active, Version: p1.Version}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfigFor(ctx, admin.ID, connectionA.ID, "", false); !errors.Is(err, ac.ErrUnverified) {
		t.Fatal("changed proxy did not invalidate identity proof")
	}
	verify(connectionA.ID)
	verify(connectionB.ID)
	mismatch, err := service.SaveConnection(ctx, admin.ID, accountB.ID, "", ac.ConnectionRequest{Name: "Mismatch", AuthType: "access_key", Region: "us-east-1", RouteMode: "socks5", ProxyID: &p1.ID, Credentials: &keyA}, meta)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Verify(ctx, admin.ID, mismatch.ID, meta)
	if err != nil || result.OK || result.Code != "ACCOUNT_MISMATCH" {
		t.Fatal("wrong AWS account was marked verified")
	}
	temporary := keyA
	temporary.SessionToken = "temporary-fixture"
	expiry := time.Now().Add(time.Hour)
	temporary.ExpiresAt = &expiry
	temp, err := service.SaveConnection(ctx, admin.ID, accountA.ID, "", ac.ConnectionRequest{Name: "Temporary", AuthType: "temporary", Region: "us-east-1", RouteMode: "direct", Credentials: &temporary}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, "UPDATE connections SET credential_expires_at=now()-interval '1 second' WHERE id=$1", temp.ID); err != nil {
		t.Fatal(err)
	}
	result, err = service.Verify(ctx, admin.ID, temp.ID, meta)
	if err != nil || result.OK || result.Code != "CREDENTIALS_EXPIRED" {
		t.Fatal("expired static session accepted")
	}
	var secretIDs []string
	allSecrets, err := store.Secrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range allSecrets {
		secretIDs = append(secretIDs, secret.ID)
	}
	if len(secretIDs) < 4 {
		t.Fatal("secrets were not persisted")
	}
	childRow, _ := store.Connection(ctx, connectionB.ID)
	if _, err := owner.ExecContext(ctx, "UPDATE connections SET secret_id=$1 WHERE id=$2", childRow.SecretID, connectionA.ID); err != nil {
		t.Fatal(err)
	}
	result, err = service.Verify(ctx, admin.ID, connectionA.ID, meta)
	if err != nil || result.OK || result.Code != "VAULT_UNAVAILABLE" {
		t.Fatal("secret reference swapped between owners")
	}
	if _, err := owner.ExecContext(ctx, "UPDATE connections SET secret_id=$1 WHERE id=$2", sourceRow.SecretID, connectionA.ID); err != nil {
		t.Fatal(err)
	}
	verify(connectionA.ID)
	verify(connectionB.ID)
	oldKey := ring.Active
	maintenance := accounts.NewMaintenance(store, path)
	rotated, err := maintenance.Maintain(ctx, "rotate", "")
	if err != nil || rotated.Reencrypted != len(allSecrets) {
		t.Fatalf("key rotation failed: %v", err)
	}
	if err := service.CheckVault(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.Maintain(ctx, "retire", oldKey); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.Maintain(ctx, "retire", rotated.ActiveKey); err == nil {
		t.Fatal("active key retired")
	}
	failedStore := accounts.NewRepository(kernel.Connection{DB: db}, func(kernel.Connection) kernel.AuditAppender { return rejectAudit{} })
	if _, err := accounts.NewMaintenance(failedStore, path).Maintain(ctx, "rotate", ""); err == nil {
		t.Fatal("audit failure ignored during rotation")
	}
	if err := service.CheckVault(ctx); err != nil {
		t.Fatal("failed rotation lost decryption keys")
	}
	if _, err := maintenance.Maintain(ctx, "reencrypt", ""); err != nil {
		t.Fatal("rotation recovery failed")
	}
	payload, _ := json.Marshal(mismatch)
	proxies, _ := service.ListProxies(ctx, admin.ID)
	proxyPayload, _ := json.Marshal(proxies)
	rows, err := pool.QueryContext(ctx, "SELECT row_to_json(s)::text FROM secrets s UNION ALL SELECT row_to_json(a)::text FROM audit_events a")
	if err != nil {
		t.Fatal(err)
	}
	all := string(payload) + string(proxyPayload)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		all += value
	}
	rows.Close()
	for _, secret := range []string{testSecretA, testExternalID, "proxy-password-alpha", "proxy-password-beta", "temporary-fixture"} {
		if strings.Contains(all, secret) {
			t.Fatal("plaintext secret escaped storage boundary")
		}
	}
	if err := service.Delete(ctx, admin.ID, "connection", connectionA.ID, connectionA.Version, meta); err == nil {
		t.Fatal("source connection removed with dependents")
	}
	for _, connection := range []ac.Connection{connectionB, mismatch, temp, connectionA} {
		if err := service.Delete(ctx, admin.ID, "connection", connection.ID, connection.Version, meta); err != nil {
			t.Fatal(err)
		}
	}
	for _, profile := range []ac.Proxy{p1, p2} {
		if err := service.Delete(ctx, admin.ID, "proxy", profile.ID, profile.Version, meta); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Delete(ctx, admin.ID, "aws_account", accountB.ID, accountB.Version, meta); err != nil {
		t.Fatal(err)
	}
	recreated, err := service.SaveAccount(ctx, admin.ID, "", ac.AccountRequest{Name: "Recreated", Partition: "aws", AWSAccountID: accountB.AWSAccountID}, meta)
	if err != nil || recreated.ID == accountB.ID {
		t.Fatal("account identity was not safely recreated")
	}
	visible, err = service.ListAccounts(ctx, viewer.ID)
	if err != nil || len(visible) != 0 {
		t.Fatal("old grant transferred to a recreated account")
	}
	var remaining int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM secrets").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("deleted connections left stored credentials")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// This interface check also ensures test clients exercise the real AWS config
// and credential-provider types rather than a parallel mock-only API.
var _ awssdk.CredentialsProvider = awssdk.CredentialsProviderFunc(nil)
