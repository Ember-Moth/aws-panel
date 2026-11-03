package service

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/auth/vault"
	awsnet "github.com/herta-network/aws-panel/internal/infra/aws"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/accounts/entity"
	"github.com/herta-network/aws-panel/internal/repository"
)

type clientEntry struct {
	fingerprint string
	config      awssdk.Config
	clients     []*http.Client
	lastUsed    time.Time
}
type guardedTransport struct {
	base  http.RoundTripper
	check func(context.Context) error
}

func (g *guardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := g.check(request.Context()); err != nil {
		return nil, err
	}
	return g.base.RoundTrip(request)
}
func (g *guardedTransport) CloseIdleConnections() {
	if closer, ok := g.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
func closeEntry(entry *clientEntry) {
	if entry != nil {
		for _, client := range entry.clients {
			client.CloseIdleConnections()
		}
	}
}
func (s *Service) Close() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	for _, entry := range s.cache {
		closeEntry(entry)
	}
	s.cache = make(map[string]*clientEntry)
}
func stamps(nodes []entity.Node, ring *vault.Keyring) (string, string) {
	type item struct {
		Node            entity.Node
		CipherHash      [32]byte
		ProxyCipherHash [32]byte
	}
	items := make([]item, 0, len(nodes))
	for _, node := range nodes {
		node.Connection.VerificationStatus = ""
		node.Connection.VerificationStage = ""
		node.Connection.VerificationCode = ""
		node.Connection.VerificationFingerprint = ""
		node.Connection.PrincipalARN = ""
		node.Connection.VerifiedAt = nil
		node.Connection.CreatedAt = time.Time{}
		node.Connection.UpdatedAt = time.Time{}
		node.Account.CreatedAt = time.Time{}
		node.Account.UpdatedAt = time.Time{}
		node.Secret.UpdatedAt = time.Time{}
		if node.Proxy != nil {
			copy := *node.Proxy
			copy.CreatedAt = time.Time{}
			copy.UpdatedAt = time.Time{}
			node.Proxy = &copy
		}
		if node.ProxySecret != nil {
			copy := *node.ProxySecret
			copy.UpdatedAt = time.Time{}
			node.ProxySecret = &copy
		}
		value := item{Node: node, CipherHash: sha256.Sum256(append(append([]byte{}, node.Secret.Nonce...), node.Secret.Ciphertext...))}
		if node.ProxySecret != nil {
			value.ProxyCipherHash = sha256.Sum256(append(append([]byte{}, node.ProxySecret.Nonce...), node.ProxySecret.Ciphertext...))
		}
		items = append(items, value)
	}
	raw, _ := json.Marshal(items)
	graphHash := sha256.Sum256(raw)
	graph := hex.EncodeToString(graphHash[:])
	if ring == nil {
		return graph, graph
	}
	keys, _ := json.Marshal(ring)
	keyHash := sha256.Sum256(keys)
	return graph, graph + "." + hex.EncodeToString(keyHash[:])
}
func (s *Service) snapshot(ctx context.Context, id string) ([]entity.Node, *vault.Keyring, string, string, error) {
	nodes, err := s.deps.Store.Graph(ctx, id)
	if err != nil {
		return nil, nil, "", "", err
	}
	if len(nodes) == 0 {
		return nil, nil, "", "", contract.ErrNotFound
	}
	for _, node := range nodes {
		if node.Account.DeletedAt != nil || !node.Account.Active || !node.Connection.Active || node.Proxy != nil && !node.Proxy.Active {
			return nil, nil, "", "", contract.ErrDisabled
		}
		if node.Connection.AuthType == "temporary" && (node.Connection.CredentialExpiresAt == nil || !node.Connection.CredentialExpiresAt.After(clock.Now())) {
			return nil, nil, "", "", contract.ErrExpired
		}
		if node.Secret.ID != node.Connection.SecretID || node.Secret.OwnerType != "connection" || node.Secret.OwnerID != node.Connection.ID || node.Secret.Purpose != "aws_credentials" {
			return nil, nil, "", "", contract.ErrVault
		}
		if node.ProxySecret != nil && (node.Proxy == nil || node.ProxySecret.OwnerType != "proxy" || node.ProxySecret.OwnerID != node.Proxy.ID || node.ProxySecret.Purpose != "proxy_auth") {
			return nil, nil, "", "", contract.ErrVault
		}
	}
	ring, err := vault.Load(s.deps.KeyringFile)
	if err != nil {
		return nil, nil, "", "", contract.ErrVault
	}
	graph, fingerprint := stamps(nodes, ring)
	return nodes, ring, graph, fingerprint, nil
}

// ConfigFor returns a guarded client configuration. Every request, including
// STS refresh and SDK retries, rechecks authorization and configuration versions.
func (s *Service) ConfigFor(ctx context.Context, actorID int64, id, region string, write bool) (awssdk.Config, error) {
	return s.configFor(ctx, actorID, id, region, write, true)
}

// ConfigForBound attaches a task lease guard to every HTTP request in the
// complete credential chain, including AssumeRole refresh and SDK retries.
func (s *Service) ConfigForBound(ctx context.Context, actorID int64, id, region string, write bool, fingerprint string, guard func(context.Context) error) (awssdk.Config, error) {
	if fingerprint == "" || guard == nil {
		return awssdk.Config{}, contract.ErrInvalid
	}
	return s.configForGuard(ctx, actorID, id, region, write, true, fingerprint, uuid.NewString(), guard)
}
func (s *Service) Inspect(ctx context.Context, actorID int64, id, region string, write bool) (contract.Snapshot, error) {
	if !validID(id) {
		return contract.Snapshot{}, contract.ErrInvalid
	}
	record, err := s.deps.Store.Connection(ctx, id)
	if err != nil {
		return contract.Snapshot{}, err
	}
	if err := s.allowed(ctx, actorID, record.AccountID, write); err != nil {
		return contract.Snapshot{}, err
	}
	nodes, _, graph, _, err := s.snapshot(ctx, id)
	if err != nil {
		return contract.Snapshot{}, err
	}
	if err := verified(nodes, true); err != nil {
		return contract.Snapshot{}, err
	}
	if region == "" {
		region = record.Region
	}
	if !regionOK(nodes[0].Account.Partition, region) {
		return contract.Snapshot{}, contract.ErrInvalid
	}
	result := contract.Snapshot{AccountID: record.AccountID, ConnectionID: id, AWSAccountID: nodes[0].Account.AWSAccountID, Partition: nodes[0].Account.Partition, Region: region, Fingerprint: graph}
	for _, node := range nodes {
		v := contract.ConnectionVersion{ID: node.Connection.ID, Version: node.Connection.Version}
		if node.Proxy != nil {
			v.ProxyID = node.Proxy.ID
			v.ProxyVersion = node.Proxy.Version
		}
		result.Chain = append(result.Chain, v)
	}
	return result, nil
}
func verified(nodes []entity.Node, includeRoot bool) error {
	start := 1
	if includeRoot {
		start = 0
	}
	for i := start; i < len(nodes); i++ {
		stamp, _ := stamps(nodes[i:], nil)
		if nodes[i].Connection.VerificationStatus != "verified" || nodes[i].Connection.VerificationFingerprint != stamp {
			return contract.ErrUnverified
		}
	}
	return nil
}
func (s *Service) configFor(ctx context.Context, actorID int64, id, region string, write, requireVerified bool) (awssdk.Config, error) {
	return s.configForGuard(ctx, actorID, id, region, write, requireVerified, "", "", nil)
}
func (s *Service) configForGuard(ctx context.Context, actorID int64, id, region string, write, requireVerified bool, expectedGraph, guardKey string, additionalGuard func(context.Context) error) (awssdk.Config, error) {
	if !validID(id) {
		return awssdk.Config{}, contract.ErrInvalid
	}
	connection, err := s.deps.Store.Connection(ctx, id)
	if err != nil {
		return awssdk.Config{}, err
	}
	if err := s.allowed(ctx, actorID, connection.AccountID, write); err != nil {
		return awssdk.Config{}, err
	}
	nodes, ring, graph, fingerprint, err := s.snapshot(ctx, id)
	if err != nil {
		return awssdk.Config{}, err
	}
	root := nodes[0]
	if expectedGraph != "" && expectedGraph != graph {
		return awssdk.Config{}, contract.ErrStale
	}
	if err := verified(nodes, requireVerified); err != nil {
		return awssdk.Config{}, err
	}
	if region == "" {
		region = root.Connection.Region
	}
	if !regionOK(root.Account.Partition, region) {
		return awssdk.Config{}, contract.ErrInvalid
	}
	key := strconv.FormatInt(actorID, 10) + "/" + id + "/" + region + "/" + strconv.FormatBool(write) + "/" + strconv.FormatBool(requireVerified)
	key += "/" + guardKey
	s.cacheMu.Lock()
	if entry := s.cache[key]; entry != nil && entry.fingerprint == fingerprint {
		entry.lastUsed = time.Now()
		cfg := entry.config
		s.cacheMu.Unlock()
		return cfg, nil
	}
	s.cacheMu.Unlock()
	check := func(ctx context.Context) error {
		if additionalGuard != nil {
			if err := additionalGuard(ctx); err != nil {
				return err
			}
		}
		if err := s.allowed(ctx, actorID, root.Account.ID, write); err != nil {
			return err
		}
		currentNodes, _, _, current, err := s.snapshot(ctx, id)
		if err != nil {
			return err
		}
		if current != fingerprint {
			return contract.ErrStale
		}
		return verified(currentNodes, requireVerified)
	}
	var provider awssdk.CredentialsProvider
	entry := &clientEntry{fingerprint: fingerprint, lastUsed: time.Now()}
	for i := len(nodes) - 1; i >= 0; i-- {
		node := nodes[i]
		raw, err := ring.Open(node.Secret.Binding(), node.Secret.Encrypted())
		if err != nil {
			closeEntry(entry)
			return awssdk.Config{}, contract.ErrVault
		}
		var secret contract.Credentials
		if json.Unmarshal(raw, &secret) != nil {
			closeEntry(entry)
			return awssdk.Config{}, contract.ErrVault
		}
		route := awsnet.Route{Mode: node.Connection.RouteMode}
		if route.Mode == "inherit" {
			route.Mode = node.Account.DefaultRouteMode
		}
		if node.Proxy != nil {
			route.Proxy = &awsnet.SOCKS5{Host: node.Proxy.Host, Port: node.Proxy.Port}
			if node.ProxySecret != nil {
				raw, err := ring.Open(node.ProxySecret.Binding(), node.ProxySecret.Encrypted())
				if err != nil {
					closeEntry(entry)
					return awssdk.Config{}, contract.ErrVault
				}
				var auth contract.ProxyAuth
				if json.Unmarshal(raw, &auth) != nil {
					closeEntry(entry)
					return awssdk.Config{}, contract.ErrVault
				}
				route.Proxy.Username = auth.Username
				route.Proxy.Password = auth.Password
			}
		}
		client, err := awsnet.NewHTTPClient(route)
		if err != nil {
			closeEntry(entry)
			return awssdk.Config{}, contract.ErrInvalid
		}
		client.Transport = &guardedTransport{base: client.Transport, check: check}
		budgetAccount := node.Account.Partition + "/" + node.Account.AWSAccountID
		stsAccount := budgetAccount
		if node.Connection.AuthType == "assume_role" && i+1 < len(nodes) {
			stsAccount = nodes[i+1].Account.Partition + "/" + nodes[i+1].Account.AWSAccountID
		}
		apiRegion := node.Connection.Region
		if i == 0 {
			apiRegion = region
		}
		client.Transport = s.budget.Wrap(client.Transport, budgetAccount, stsAccount, apiRegion, node.Connection.Region)
		entry.clients = append(entry.clients, client)
		if node.Connection.AuthType == "assume_role" {
			if provider == nil || s.deps.STS == nil {
				closeEntry(entry)
				return awssdk.Config{}, contract.ErrUnavailable
			}
			sourceConfig, err := awsnet.NewConfig(node.Connection.Region, provider, client)
			if err != nil {
				closeEntry(entry)
				return awssdk.Config{}, contract.ErrInvalid
			}
			role, externalID := node.Connection.RoleARN, secret.ExternalID
			sessionName := "aws-panel-m" + strconv.FormatInt(actorID, 10) + "-" + node.Connection.ID[:8]
			provider = awssdk.NewCredentialsCache(awssdk.CredentialsProviderFunc(func(ctx context.Context) (awssdk.Credentials, error) {
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				if err := check(ctx); err != nil {
					return awssdk.Credentials{}, err
				}
				return s.deps.STS.Assume(ctx, sourceConfig, role, sessionName, externalID)
			}), func(options *awssdk.CredentialsCacheOptions) { options.ExpiryWindow = time.Minute })
		} else {
			credentials := awssdk.Credentials{AccessKeyID: secret.AccessKeyID, SecretAccessKey: secret.SecretAccessKey, SessionToken: secret.SessionToken, Source: "AWS Panel connection"}
			if node.Connection.AuthType == "temporary" {
				if secret.ExpiresAt == nil {
					closeEntry(entry)
					return awssdk.Config{}, contract.ErrExpired
				}
				credentials.CanExpire = true
				credentials.Expires = *secret.ExpiresAt
			}
			provider = awssdk.NewCredentialsCache(awssdk.CredentialsProviderFunc(func(ctx context.Context) (awssdk.Credentials, error) {
				if err := check(ctx); err != nil {
					return awssdk.Credentials{}, err
				}
				if credentials.CanExpire && !credentials.Expires.After(clock.Now()) {
					return awssdk.Credentials{}, contract.ErrExpired
				}
				return credentials, nil
			}))
		}
		if i == 0 {
			entry.config, err = awsnet.NewConfig(region, provider, client)
			if err != nil {
				closeEntry(entry)
				return awssdk.Config{}, contract.ErrInvalid
			}
		}
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if previous := s.cache[key]; previous != nil {
		closeEntry(previous)
	}
	if len(s.cache) >= 128 {
		var oldestKey string
		var oldest *clientEntry
		for candidate, value := range s.cache {
			if oldest == nil || value.lastUsed.Before(oldest.lastUsed) {
				oldestKey, oldest = candidate, value
			}
		}
		closeEntry(oldest)
		delete(s.cache, oldestKey)
	}
	s.cache[key] = entry
	return entry.config, nil
}
func verificationFailure(err error, proxy bool) (string, string, string) {
	var fault *contract.Error
	if errors.As(err, &fault) {
		return "configuration", fault.Code, fault.Message
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		code := api.ErrorCode()
		if len(code) > 80 || strings.IndexFunc(code, func(r rune) bool {
			return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-')
		}) >= 0 {
			code = "AWS_ERROR"
		}
		return "aws", code, "AWS 拒绝了身份验证，请检查凭据、角色信任和到期时间"
	}
	var certificate x509.UnknownAuthorityError
	var tlsVerification *tls.CertificateVerificationError
	var hostname x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	if errors.As(err, &certificate) || errors.As(err, &tlsVerification) || errors.As(err, &hostname) || errors.As(err, &invalidCertificate) {
		return "tls", "TLS_VALIDATION_FAILED", "AWS TLS 证书验证失败"
	}
	if proxy && (errors.Is(err, awsnet.ErrProxyAuthentication) || strings.Contains(strings.ToLower(err.Error()), "username/password authentication failed")) {
		return "proxy", "PROXY_AUTH_FAILED", "代理用户名或密码验证失败"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "network", "TIMEOUT", "连接超时，请检查出口"
	}
	if proxy {
		return "proxy", "PROXY_CONNECTION_FAILED", "代理或代理到 AWS 的链路不可用"
	}
	return "network", "CONNECTION_FAILED", "无法连接 AWS 身份服务"
}
func (s *Service) Verify(ctx context.Context, actorID int64, id string, meta auth.RequestMeta) (contract.Verification, error) {
	actor, err := s.actor(ctx, actorID, true)
	if err != nil {
		return contract.Verification{}, err
	}
	if !validID(id) {
		return contract.Verification{}, contract.ErrInvalid
	}
	record, err := s.deps.Store.Connection(ctx, id)
	if err != nil {
		return contract.Verification{}, err
	}
	nodes, err := s.deps.Store.Graph(ctx, id)
	if err != nil {
		return contract.Verification{}, err
	}
	graph, _ := stamps(nodes, nil)
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := contract.Verification{CheckedAt: clock.Now()}
	cfg, callErr := s.configFor(ctx, actorID, id, "", false, false)
	if callErr == nil {
		if s.deps.STS == nil {
			callErr = contract.ErrUnavailable
		} else {
			identity, err := s.deps.STS.Identity(ctx, cfg)
			callErr = err
			if err == nil {
				parsed, parseErr := arn.Parse(identity.ARN)
				if parseErr != nil || identity.AccountID != nodes[0].Account.AWSAccountID || parsed.Partition != nodes[0].Account.Partition || parsed.AccountID != identity.AccountID {
					result.Stage = "identity"
					result.Code = "ACCOUNT_MISMATCH"
					result.Message = "凭据返回的账号或分区与配置不一致"
				} else {
					result.OK = true
					result.Stage = "identity"
					result.Code = "VERIFIED"
					result.Message = "AWS 账号身份已验证"
					result.AccountID = identity.AccountID
					result.PrincipalARN = identity.ARN
					result.RequestID = identity.RequestID
				}
			}
		}
	}
	if callErr != nil {
		result.Stage, result.Code, result.Message = verificationFailure(callErr, len(nodes) > 0 && nodes[0].Proxy != nil)
	}
	result.CheckedAt = clock.Now()
	persist, persistCancel := context.WithTimeout(parent, 5*time.Second)
	defer persistCancel()
	err = s.deps.Store.InAccountsTx(persist, func(tx repository.AccountsTx) error {
		ctx := persist
		if err := tx.LockAccounts(ctx); err != nil {
			return err
		}
		latest, err := tx.Graph(ctx, id)
		if err != nil {
			return err
		}
		current, _ := stamps(latest, nil)
		if current != graph {
			return contract.ErrStale
		}
		record.VerificationStatus = "failed"
		if result.OK {
			record.VerificationStatus = "verified"
		}
		record.VerificationStage = result.Stage
		record.VerificationCode = result.Code
		record.VerificationFingerprint = graph
		record.PrincipalARN = result.PrincipalARN
		record.VerifiedAt = &result.CheckedAt
		if err := tx.SaveVerification(ctx, record); err != nil {
			return err
		}
		entry := audit(meta, actor, "accounts.connection.verified", "connection", id, map[string]string{"result": result.Code, "stage": result.Stage})
		if !result.OK {
			entry.Outcome = "failure"
		}
		return tx.AppendAudit(ctx, entry)
	})
	return result, err
}
