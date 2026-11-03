package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/herta-network/aws-panel/internal/module/accounts/entity"
	"testing"
	"time"
)

// The schema-8 fingerprint shape must remain stable when new default-route
// columns are backfilled; explicit existing routes must not need re-verification.
func TestSchema8FingerprintCompatibility(t *testing.T) {
	type oldAccount struct {
		ID, Partition, AWSAccountID, Name string
		Active                            bool
		Version                           int64
		CreatedAt, UpdatedAt              time.Time
		DeletedAt                         *time.Time
	}
	type oldProxy struct {
		ID, Name, Host       string
		Port                 int
		SecretID             *string
		Active               bool
		Version              int64
		CreatedAt, UpdatedAt time.Time
	}
	type oldNode struct {
		Account     oldAccount
		Connection  entity.Connection
		Secret      entity.Secret
		Proxy       *oldProxy
		ProxySecret *entity.Secret
	}
	type oldItem struct {
		Node                        oldNode
		CipherHash, ProxyCipherHash [32]byte
	}
	proxyID := "proxy-fixture"
	node := entity.Node{Account: entity.Account{ID: "account-fixture", Partition: "aws", AWSAccountID: "111111111111", Name: "Fixture", Active: true, Version: 1, DefaultRouteMode: "socks5", DefaultProxyID: &proxyID, DefaultProxyName: "Fixture proxy"}, Connection: entity.Connection{ID: "connection-fixture", AccountID: "account-fixture", RouteMode: "socks5", ProxyID: &proxyID, Active: true, Version: 1}, Proxy: &entity.Proxy{ID: proxyID, Name: "Fixture proxy", Host: "127.0.0.1", Port: 1080, Protocol: "socks5", Active: true, Version: 1}}
	legacy := []oldItem{{Node: oldNode{Account: oldAccount{ID: node.Account.ID, Partition: node.Account.Partition, AWSAccountID: node.Account.AWSAccountID, Name: node.Account.Name, Active: true, Version: 1}, Connection: node.Connection, Proxy: &oldProxy{ID: proxyID, Name: node.Proxy.Name, Host: node.Proxy.Host, Port: 1080, Active: true, Version: 1}}, CipherHash: sha256.Sum256(nil)}}
	body, _ := json.Marshal(legacy)
	digest := sha256.Sum256(body)
	actual, _ := stamps([]entity.Node{node}, nil)
	if actual != hex.EncodeToString(digest[:]) {
		t.Fatal("new proxy columns changed legacy fingerprints")
	}
	node.Account.Version++
	changed, _ := stamps([]entity.Node{node}, nil)
	if changed == actual {
		t.Fatal("versioned account route edit did not invalidate fingerprint")
	}
}
