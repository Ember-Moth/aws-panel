package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/auth/vault"
	"github.com/herta-network/aws-panel/internal/module/accounts"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/identity"
	ic "github.com/herta-network/aws-panel/internal/module/identity/contract"
	"github.com/herta-network/aws-panel/internal/module/inventory"
	iv "github.com/herta-network/aws-panel/internal/module/inventory/contract"
	"github.com/herta-network/aws-panel/internal/module/operations"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/module/platform"
	"github.com/herta-network/aws-panel/internal/module/regions"
	"github.com/herta-network/aws-panel/internal/repository"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

const inventoryKeyB = "AKIAINVENTORYSECOND12"
const inventorySecretB = "inventory-fixture-secondary-secret-123456"

type inventoryAWS struct {
	t              *testing.T
	mu             sync.Mutex
	modes          map[string]string
	calls          map[string]int
	pageTwoEntered chan struct{}
	pageTwoRelease chan struct{}
	blockOnce      sync.Once
	optStates      map[string]string
	enableCalls    map[string]int
	enableModes    map[string]string
	optPolls       map[string]int
	extra          func(http.ResponseWriter, *http.Request, string, string, string, map[string]any) bool
}

func newInventoryAWS(t *testing.T) *inventoryAWS {
	return &inventoryAWS{t: t, modes: map[string]string{}, calls: map[string]int{}, optStates: map[string]string{}, enableCalls: map[string]int{}, enableModes: map[string]string{}, optPolls: map[string]int{}}
}
func (f *inventoryAWS) List(ctx context.Context, cfg aws.Config) ([]regions.State, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return regions.NewCloud().List(ctx, cfg)
}
func (f *inventoryAWS) Status(ctx context.Context, cfg aws.Config, region string) (regions.State, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return regions.NewCloud().Status(ctx, cfg, region)
}
func (f *inventoryAWS) Enable(ctx context.Context, cfg aws.Config, region string) (string, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return regions.NewCloud().Enable(ctx, cfg, region)
}
func (f *inventoryAWS) enables() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, value := range f.enableCalls {
		count += value
	}
	return count
}
func (f *inventoryAWS) set(account, region, mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modes[account+"/"+region] = mode
}
func (f *inventoryAWS) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, value := range f.calls {
		n += value
	}
	return n
}
func (f *inventoryAWS) Identity(ctx context.Context, cfg aws.Config) (accounts.IdentityResult, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return accounts.NewSTS().Identity(ctx, cfg)
}
func (f *inventoryAWS) Assume(ctx context.Context, cfg aws.Config, role, name, external string) (aws.Credentials, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return accounts.NewSTS().Assume(ctx, cfg, role, name, external)
}
func (f *inventoryAWS) Fetch(ctx context.Context, cfg aws.Config, req inventory.FetchRequest) (inventory.Snapshot, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return inventory.NewCloud().Fetch(ctx, cfg, req)
}
func (f *inventoryAWS) handler(expectedAccount string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			f.t.Error(err)
			return
		}
		authorization := r.Header.Get("Authorization")
		_, credential, ok := strings.Cut(authorization, "Credential=")
		if !ok {
			f.t.Error("unsigned inventory request")
			w.WriteHeader(403)
			return
		}
		credential, _, _ = strings.Cut(credential, ",")
		parts := strings.Split(credential, "/")
		if len(parts) != 5 {
			f.t.Error("invalid signing scope")
			w.WriteHeader(403)
			return
		}
		key, region, service := parts[0], parts[2], parts[3]
		account, secret := "111111111111", testSecretA
		if key == inventoryKeyB {
			account, secret = "222222222222", inventorySecretB
		} else if key != testKeyA {
			f.t.Error("unknown inventory credential")
			w.WriteHeader(403)
			return
		}
		if account != expectedAccount {
			f.t.Error("inventory credentials crossed proxy routes")
			w.WriteHeader(403)
			return
		}
		clone := r.Clone(r.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = r.Host
		clone.RequestURI = ""
		clone.Header.Del("Authorization")
		_, signed, _ := strings.Cut(authorization, "SignedHeaders=")
		signed, _, _ = strings.Cut(signed, ",")
		for header := range clone.Header {
			if !strings.Contains(";"+signed+";", ";"+strings.ToLower(header)+";") {
				clone.Header.Del(header)
			}
		}
		signedAt, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
		if err != nil {
			f.t.Error("missing signing time")
			w.WriteHeader(403)
			return
		}
		sum := sha256.Sum256(body)
		err = v4.NewSigner().SignHTTP(r.Context(), aws.Credentials{AccessKeyID: key, SecretAccessKey: secret}, clone, hex.EncodeToString(sum[:]), service, region, signedAt)
		if err != nil || clone.Header.Get("Authorization") != authorization {
			f.t.Error("invalid SDK inventory signature")
			w.WriteHeader(403)
			return
		}
		f.mu.Lock()
		f.calls[account+"/"+service+"/"+region]++
		mode := f.modes[account+"/"+region]
		f.mu.Unlock()
		w.Header().Set("X-Amzn-Requestid", "fixture-inventory-request")
		if service == "sts" {
			w.Header().Set("Content-Type", "text/xml")
			fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>%s</Account><Arn>arn:aws:iam::%s:user/InventoryFixture</Arn><UserId>fixture</UserId></GetCallerIdentityResult><ResponseMetadata><RequestId>fixture-sts</RequestId></ResponseMetadata></GetCallerIdentityResponse>`, account, account)
			return
		}
		var input map[string]any
		if json.Unmarshal(body, &input) != nil {
			f.t.Error("invalid inventory request body")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		if f.extra != nil && f.extra(w, r, account, region, service, input) {
			return
		}
		if service == "account" {
			if region != "us-east-1" || input["AccountId"] != nil {
				f.t.Error("Account API did not use standalone account scope")
				w.WriteHeader(400)
				return
			}
			if r.URL.Path == "/getRegionOptStatus" || r.URL.Path == "/enableRegion" {
				wanted, _ := input["RegionName"].(string)
				key := account + "/" + wanted
				f.mu.Lock()
				state := f.optStates[key]
				if state == "" {
					state = "DISABLED"
					if wanted == "us-east-1" {
						state = "ENABLED_BY_DEFAULT"
					}
				}
				if r.URL.Path == "/getRegionOptStatus" {
					if state == "ENABLING" {
						f.optPolls[key]++
						if f.optPolls[key] >= 2 {
							state = "ENABLED"
							f.optStates[key] = state
						}
					}
					f.mu.Unlock()
					json.NewEncoder(w).Encode(map[string]any{"RegionName": wanted, "RegionOptStatus": state})
					return
				}
				f.enableCalls[key]++
				mode := f.enableModes[key]
				if mode == "reject" {
					f.mu.Unlock()
					w.WriteHeader(403)
					json.NewEncoder(w).Encode(map[string]any{"__type": "AccessDeniedException", "message": secret})
					return
				}
				f.optStates[key] = "ENABLING"
				f.mu.Unlock()
				if mode == "lost_response" {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						f.t.Error(err)
						return
					}
					connection.Close()
					return
				}
				w.WriteHeader(200)
				return
			}
			if r.URL.Path != "/listRegions" {
				f.t.Error("unexpected Account API operation")
				w.WriteHeader(400)
				return
			}
			if input["NextToken"] == nil {
				json.NewEncoder(w).Encode(map[string]any{"Regions": []any{map[string]any{"RegionName": "us-east-1", "RegionOptStatus": "ENABLED_BY_DEFAULT"}}, "NextToken": "regions-page-2"})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"Regions": []any{map[string]any{"RegionName": "ap-east-1", "RegionOptStatus": "DISABLED"}}})
			}
			return
		}
		if service != "lightsail" {
			f.t.Error("unexpected AWS service")
			w.WriteHeader(400)
			return
		}
		action := r.Header.Get("X-Amz-Target")
		if strings.HasSuffix(action, ".GetRegions") {
			json.NewEncoder(w).Encode(map[string]any{"regions": []any{map[string]any{"name": "us-east-1", "displayName": "Virginia", "availabilityZones": []any{map[string]any{"zoneName": "us-east-1a", "state": "available"}}}, map[string]any{"name": "us-west-2", "displayName": "Oregon", "availabilityZones": []any{map[string]any{"zoneName": "us-west-2a", "state": "available"}}}}})
			return
		}
		if !strings.HasSuffix(action, ".GetInstances") {
			f.t.Error("unexpected Lightsail operation")
			w.WriteHeader(400)
			return
		}
		second := input["pageToken"] != nil
		if mode == "slow_second" && second {
			f.blockOnce.Do(func() { close(f.pageTwoEntered) })
			select {
			case <-f.pageTwoRelease:
			case <-r.Context().Done():
				return
			}
		}
		if mode == "denied_second" && second {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"__type": "AccessDeniedException", "message": "private AWS error " + secret})
			return
		}
		if mode == "empty" {
			json.NewEncoder(w).Encode(map[string]any{"instances": []any{}})
			return
		}
		index := 1
		if second {
			index = 2
		}
		owner := account
		if mode == "wrong_account" {
			owner = "999999999999"
		}
		name := fmt.Sprintf("fixture-%s-%d", account, index)
		if mode == "denied_second" && !second {
			name = "partial-page-must-not-publish"
		}
		instance := map[string]any{"arn": fmt.Sprintf("arn:aws:lightsail:%s:%s:Instance/fixture-%d", region, owner, index), "name": name, "blueprintId": "debian_12", "blueprintName": "Debian 12", "bundleId": "small_3_0", "createdAt": time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix(), "location": map[string]any{"regionName": region, "availabilityZone": region + "a"}, "state": map[string]any{"name": "running", "code": 16}, "hardware": map[string]any{"cpuCount": 2, "ramSizeInGb": 2.0}, "publicIpAddress": "127.0.0.10", "privateIpAddress": "10.4.0.1", "ipAddressType": "dualstack", "tags": []any{map[string]any{"key": "private-tag", "value": secret}}, "username": "admin"}
		items := []any{instance}
		if mode == "many" {
			items = []any{}
			offset := 0
			if second {
				offset = 13
			}
			for i := 1; i <= 13; i++ {
				copy := map[string]any{}
				for key, value := range instance {
					copy[key] = value
				}
				copy["arn"] = fmt.Sprintf("arn:aws:lightsail:%s:%s:Instance/fixture-%d", region, owner, offset+i)
				copy["name"] = fmt.Sprintf("fixture-%s-%02d", account, offset+i)
				items = append(items, copy)
			}
		}
		output := map[string]any{"instances": items}
		if !second || mode == "repeat_token" {
			output["nextPageToken"] = "instances-page-2"
		}
		json.NewEncoder(w).Encode(output)
	})
}

type inventoryBindings struct{ accounts accounts.Service }

func (b inventoryBindings) Inspect(ctx context.Context, actor int64, id, region string, write bool) (oc.Binding, error) {
	value, err := b.accounts.Inspect(ctx, actor, id, region, write)
	if err != nil {
		if errors.Is(err, ac.ErrForbidden) {
			return oc.Binding{}, oc.ErrForbidden
		}
		return oc.Binding{}, oc.ErrBinding
	}
	result := oc.Binding{AccountID: value.AccountID, ConnectionID: value.ConnectionID, AWSAccountID: value.AWSAccountID, Partition: value.Partition, Region: value.Region, Fingerprint: value.Fingerprint}
	for _, node := range value.Chain {
		result.Chain = append(result.Chain, oc.ConnectionVersion{ID: node.ID, Version: node.Version, ProxyID: node.ProxyID, ProxyVersion: node.ProxyVersion})
	}
	return result, nil
}

type inventoryHarness struct {
	op                       *operationHarness
	service                  inventory.Service
	store                    repository.InventoryStore
	accounts                 accounts.Service
	accountA, accountB       ac.Account
	connectionA, connectionB ac.Connection
	proxyA, proxyB           ac.Proxy
	cloud                    *inventoryAWS
	db                       *sql.DB
	stopProxyA               func()
	accountStore             repository.AccountsStore
	keyringPath              string
}

func newInventory(t *testing.T) *inventoryHarness {
	t.Helper()
	db, _, owner := identityDatabase(t)
	ctx := context.Background()
	accountStore := accounts.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender, operations.NewReferenceGuard)
	catalog := accounts.NewCatalog(accountStore)
	members := identity.New(identity.Deps{Store: identity.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender), Limiter: allowLogin{}, SessionTTL: time.Hour, AccountExists: catalog.Exists, AccountLabel: catalog.Label})
	admin, err := members.Bootstrap(ctx, ic.CreateMemberRequest{Username: "admin", DisplayName: "Admin", Password: "inventory-test-admin-passphrase"}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	cloud := newInventoryAWS(t)
	serverA := httptest.NewServer(cloud.handler("111111111111"))
	serverB := httptest.NewServer(cloud.handler("222222222222"))
	t.Cleanup(serverA.Close)
	t.Cleanup(serverB.Close)
	proxyA := newSOCKSForwarder(t, "inventory-a", "inventory-proxy-secret-a", "aws-fixture.invalid", strings.TrimPrefix(serverA.URL, "http://"))
	proxyB := newSOCKSForwarder(t, "inventory-b", "inventory-proxy-secret-b", "aws-fixture.invalid", strings.TrimPrefix(serverB.URL, "http://"))
	path := filepath.Join(t.TempDir(), "keyring.json")
	ring, _ := vault.Generate()
	if err := ring.Write(path, true); err != nil {
		t.Fatal(err)
	}
	accountService := accounts.New(accounts.Deps{Store: accountStore, Access: members, KeyringFile: path, STS: cloud})
	t.Cleanup(accountService.Close)
	a, err := accountService.SaveAccount(ctx, admin.ID, "", ac.AccountRequest{Name: "Inventory A", Partition: "aws", AWSAccountID: "111111111111"}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := accountService.SaveAccount(ctx, admin.ID, "", ac.AccountRequest{Name: "Inventory B", Partition: "aws", AWSAccountID: "222222222222"}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	pa, err := accountService.SaveProxy(ctx, admin.ID, "", ac.ProxyRequest{Name: "Inventory A route", Host: proxyA.host, Port: proxyA.port, Auth: &ac.ProxyAuth{Username: "inventory-a", Password: "inventory-proxy-secret-a"}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := accountService.SaveProxy(ctx, admin.ID, "", ac.ProxyRequest{Name: "Inventory B route", Host: proxyB.host, Port: proxyB.port, Auth: &ac.ProxyAuth{Username: "inventory-b", Password: "inventory-proxy-secret-b"}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := accountService.SaveConnection(ctx, admin.ID, a.ID, "", ac.ConnectionRequest{Name: "Inventory A", AuthType: "access_key", Region: "us-east-1", RouteMode: "socks5", ProxyID: &pa.ID, Credentials: &ac.Credentials{AccessKeyID: testKeyA, SecretAccessKey: testSecretA}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	cb, err := accountService.SaveConnection(ctx, admin.ID, b.ID, "", ac.ConnectionRequest{Name: "Inventory B", AuthType: "access_key", Region: "us-east-1", RouteMode: "socks5", ProxyID: &pb.ID, Credentials: &ac.Credentials{AccessKeyID: inventoryKeyB, SecretAccessKey: inventorySecretB}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	for _, connection := range []ac.Connection{ca, cb} {
		result, err := accountService.Verify(ctx, admin.ID, connection.ID, auth.RequestMeta{})
		if err != nil || !result.OK {
			t.Fatal("inventory connection failed verification", err, result.Code)
		}
	}
	opStore := operations.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender, accounts.NewBindingGuard)
	invStore := inventory.NewRepository(kernel.Connection{DB: db}, platform.NewAuditAppender, operations.NewExecutionGuard)
	queue := &capturedPublisher{}
	jobService, err := operations.New(operations.Deps{Store: opStore, Access: members, Connections: inventoryBindings{accountService}, Publisher: queue, Executors: []oc.Executor{inventory.NewExecutor(inventory.ExecutorDeps{Store: invStore, Clients: accountService, Cloud: cloud})}, RedeliveryAfter: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	invService := inventory.New(inventory.Deps{Store: invStore, Accounts: accountService, Access: members, Jobs: jobService, Observer: operations.NewObserver(opStore)})
	return &inventoryHarness{op: &operationHarness{store: opStore, service: jobService, members: members, admin: admin.ID, queue: queue, owner: owner}, service: invService, store: invStore, accounts: accountService, accountA: a, accountB: b, connectionA: ca, connectionB: cb, proxyA: pa, proxyB: pb, cloud: cloud, db: owner, stopProxyA: proxyA.stop, accountStore: accountStore, keyringPath: path}
}
func (h *inventoryHarness) refresh(t *testing.T, targets ...iv.RefreshTarget) oc.Job {
	t.Helper()
	job, err := h.service.Refresh(context.Background(), h.op.admin, iv.RefreshRequest{Targets: targets, IdempotencyKey: uuid.NewString()}, auth.RequestMeta{RequestID: "inventory-refresh-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return job
}
