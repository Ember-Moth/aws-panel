package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/auth/vault"
	awsnet "github.com/herta-network/aws-panel/internal/infra/aws"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/accounts/entity"
	"github.com/herta-network/aws-panel/internal/repository"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

type Access interface {
	Principal(context.Context, int64) (auth.Principal, error)
	AccountScope(context.Context, int64) ([]string, error)
	AccountAccess(context.Context, int64, string, bool) (bool, error)
}
type IdentityResult struct{ AccountID, ARN, RequestID string }
type STS interface {
	Identity(context.Context, awssdk.Config) (IdentityResult, error)
	Assume(context.Context, awssdk.Config, string, string, string) (awssdk.Credentials, error)
}
type Deps struct {
	Store       repository.AccountsStore
	Access      Access
	KeyringFile string
	STS         STS
}
type Service struct {
	deps    Deps
	cacheMu sync.Mutex
	cache   map[string]*clientEntry
	budget  *awsnet.Budget
}

func New(deps Deps) *Service {
	return &Service{deps: deps, cache: make(map[string]*clientEntry), budget: awsnet.NewBudget()}
}
func (s *Service) Exists(ctx context.Context, id string) (bool, error) {
	if _, err := uuid.Parse(id); err != nil {
		return false, nil
	}
	record, err := s.deps.Store.Account(ctx, id)
	if errors.Is(err, contract.ErrNotFound) {
		return false, nil
	}
	return err == nil && record.DeletedAt == nil, err
}
func (s *Service) Label(ctx context.Context, id string) (string, error) {
	if !validID(id) {
		return "", contract.ErrInvalid
	}
	value, err := s.deps.Store.Account(ctx, id)
	if err != nil {
		return "", err
	}
	if value.DeletedAt != nil {
		return "", contract.ErrNotFound
	}
	return value.Name + " · " + value.AWSAccountID, nil
}
func (s *Service) actor(ctx context.Context, id int64, admin bool) (auth.Principal, error) {
	if s.deps.Access == nil {
		return auth.Principal{}, contract.ErrForbidden
	}
	actor, err := s.deps.Access.Principal(ctx, id)
	if errors.Is(err, auth.ErrDenied) {
		return actor, contract.ErrForbidden
	}
	if err != nil {
		return actor, contract.ErrUnavailable
	}
	if admin && actor.Role != "admin" {
		return actor, contract.ErrForbidden
	}
	return actor, nil
}
func (s *Service) allowed(ctx context.Context, id int64, account string, write bool) error {
	ok, err := s.deps.Access.AccountAccess(ctx, id, account, write)
	if err != nil {
		return contract.ErrUnavailable
	}
	if !ok {
		return contract.ErrForbidden
	}
	return nil
}
func validID(id string) bool { _, err := uuid.Parse(id); return err == nil }
func nameOK(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= 80 && strings.IndexFunc(value, unicode.IsControl) < 0
}

var accountPattern = regexp.MustCompile(`^[0-9]{12}$`)
var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)

func partitionOK(value string) bool {
	return value == "aws" || value == "aws-cn" || value == "aws-us-gov"
}
func regionOK(partition, region string) bool {
	if !regionPattern.MatchString(region) {
		return false
	}
	if partition == "aws-cn" {
		return strings.HasPrefix(region, "cn-")
	}
	if partition == "aws-us-gov" {
		return strings.HasPrefix(region, "us-gov-")
	}
	return !strings.HasPrefix(region, "cn-") && !strings.HasPrefix(region, "us-gov-")
}
func accountView(v entity.Account) contract.Account {
	return contract.Account{ID: v.ID, Partition: v.Partition, AWSAccountID: v.AWSAccountID, Name: v.Name, Active: v.Active, Version: v.Version, CreatedAt: clock.In(v.CreatedAt), UpdatedAt: clock.In(v.UpdatedAt)}
}
func timeView(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	local := clock.In(*value)
	return &local
}
func connectionView(v entity.Connection, admin bool) contract.Connection {
	result := contract.Connection{ID: v.ID, AccountID: v.AccountID, Name: v.Name, Region: v.Region, Active: v.Active, Version: v.Version,
		Status: v.VerificationStatus, Stage: v.VerificationStage, Code: v.VerificationCode, PrincipalARN: v.PrincipalARN, VerifiedAt: timeView(v.VerifiedAt)}
	if admin {
		result.AuthType = v.AuthType
		result.RouteMode = v.RouteMode
		result.ProxyID = v.ProxyID
		result.SourceConnectionID = v.SourceConnectionID
		result.RoleARN = v.RoleARN
		result.CredentialExpiresAt = timeView(v.CredentialExpiresAt)
		if v.AccessKeySuffix != "" {
			result.AccessKeyMasked = "••••" + v.AccessKeySuffix
		}
	}
	return result
}
func proxyView(v entity.Proxy, count int64) contract.Proxy {
	return contract.Proxy{ID: v.ID, Name: v.Name, Host: v.Host, Port: v.Port, HasAuth: v.SecretID != nil, Active: v.Active, Version: v.Version, Connections: count}
}
func audit(meta auth.RequestMeta, actor auth.Principal, action, kind, id string, details map[string]string) kernel.AuditEntry {
	var actorID *int64
	if actor.ID > 0 {
		actorID = &actor.ID
	}
	return kernel.AuditEntry{ActorID: actorID, ActorName: actor.Username, Action: action, TargetType: kind, TargetID: id, Outcome: "success",
		RequestID: meta.RequestID, ClientIP: meta.ClientIP, Metadata: details, OccurredAt: clock.Now()}
}
func (s *Service) VaultReady() bool { _, err := vault.Load(s.deps.KeyringFile); return err == nil }
func (s *Service) CheckVault(ctx context.Context) error {
	secrets, err := s.deps.Store.Secrets(ctx)
	if err != nil {
		return err
	}
	if len(secrets) == 0 {
		if s.deps.KeyringFile == "" {
			return nil
		}
		_, err := vault.Load(s.deps.KeyringFile)
		if err != nil {
			return contract.ErrVault
		}
		return nil
	}
	ring, err := vault.Load(s.deps.KeyringFile)
	if err != nil {
		return contract.ErrVault
	}
	for _, secret := range secrets {
		if _, err := ring.Open(secret.Binding(), secret.Encrypted()); err != nil {
			return contract.ErrVault
		}
	}
	return nil
}
func (s *Service) ListAccounts(ctx context.Context, actorID int64) ([]contract.Account, error) {
	actor, err := s.actor(ctx, actorID, false)
	if err != nil {
		return nil, err
	}
	var ids []string
	if actor.Role != "admin" {
		ids, err = s.deps.Access.AccountScope(ctx, actorID)
		if err != nil {
			return nil, contract.ErrUnavailable
		}
	}
	rows, err := s.deps.Store.Accounts(ctx, ids, actor.Role == "admin")
	if err != nil {
		return nil, err
	}
	result := make([]contract.Account, 0, len(rows))
	for _, row := range rows {
		result = append(result, accountView(row))
	}
	return result, nil
}
func (s *Service) AccountDetail(ctx context.Context, actorID int64, id string) (*contract.AccountDetail, error) {
	actor, err := s.actor(ctx, actorID, false)
	if err != nil {
		return nil, err
	}
	if !validID(id) {
		return nil, contract.ErrInvalid
	}
	if err := s.allowed(ctx, actorID, id, false); err != nil {
		return nil, err
	}
	account, err := s.deps.Store.Account(ctx, id)
	if err != nil {
		return nil, err
	}
	if account.DeletedAt != nil {
		return nil, contract.ErrNotFound
	}
	if !account.Active && actor.Role != "admin" {
		return nil, contract.ErrForbidden
	}
	rows, err := s.deps.Store.Connections(ctx, id)
	if err != nil {
		return nil, err
	}
	result := &contract.AccountDetail{Account: accountView(account), Connections: []contract.Connection{}, CanManage: actor.Role == "admin", VaultReady: s.VaultReady()}
	for _, row := range rows {
		row = s.currentStatus(ctx, row)
		result.Connections = append(result.Connections, connectionView(row, result.CanManage))
	}
	return result, nil
}
func (s *Service) SaveAccount(ctx context.Context, actorID int64, id string, req contract.AccountRequest, meta auth.RequestMeta) (contract.Account, error) {
	actor, err := s.actor(ctx, actorID, true)
	if err != nil {
		return contract.Account{}, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if !nameOK(req.Name) || !partitionOK(req.Partition) || !accountPattern.MatchString(req.AWSAccountID) {
		return contract.Account{}, contract.Invalid("请填写名称、分区和 12 位 AWS 账号 ID")
	}
	var record entity.Account
	err = s.deps.Store.InAccountsTx(ctx, func(tx repository.AccountsTx) error {
		if err := tx.LockAccounts(ctx); err != nil {
			return err
		}
		expected := int64(0)
		now := clock.Now()
		action := "accounts.account.created"
		if id == "" {
			record = entity.Account{ID: uuid.NewString(), Partition: req.Partition, AWSAccountID: req.AWSAccountID, Active: true, Version: 1, CreatedAt: now}
		} else {
			if !validID(id) || req.Active == nil {
				return contract.ErrInvalid
			}
			var err error
			record, err = tx.Account(ctx, id)
			if err != nil {
				return err
			}
			if record.DeletedAt != nil {
				return contract.ErrNotFound
			}
			if record.Version != req.Version {
				return contract.ErrConflict
			}
			if record.Partition != req.Partition || record.AWSAccountID != req.AWSAccountID {
				return contract.Invalid("已创建账号的分区和 AWS ID 不可修改")
			}
			expected = record.Version
			record.Version++
			record.Active = *req.Active
			action = "accounts.account.updated"
		}
		record.Name = req.Name
		if expected == 0 && req.Active != nil {
			record.Active = *req.Active
		}
		record.UpdatedAt = now
		if err := tx.SaveAccount(ctx, record, expected); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit(meta, actor, action, "aws_account", record.ID, map[string]string{"version": strconv.FormatInt(record.Version, 10)}))
	})
	return accountView(record), err
}
func (s *Service) ListProxies(ctx context.Context, actorID int64) ([]contract.Proxy, error) {
	if _, err := s.actor(ctx, actorID, true); err != nil {
		return nil, err
	}
	rows, err := s.deps.Store.Proxies(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]contract.Proxy, 0, len(rows))
	for _, row := range rows {
		count, err := s.deps.Store.ProxyUsage(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, proxyView(row, count))
	}
	return result, nil
}
func (s *Service) Proxy(ctx context.Context, actorID int64, id string) (contract.Proxy, error) {
	if _, err := s.actor(ctx, actorID, true); err != nil {
		return contract.Proxy{}, err
	}
	if !validID(id) {
		return contract.Proxy{}, contract.ErrInvalid
	}
	row, err := s.deps.Store.Proxy(ctx, id)
	if err != nil {
		return contract.Proxy{}, err
	}
	count, err := s.deps.Store.ProxyUsage(ctx, id)
	if err != nil {
		return contract.Proxy{}, err
	}
	impact, err := s.deps.Store.ProxyImpact(ctx, id)
	if err != nil {
		return contract.Proxy{}, err
	}
	result := proxyView(row, count)
	for _, connection := range impact {
		result.Affected = append(result.Affected, connectionView(connection, true))
	}
	return result, nil
}
func hostOK(value string) bool {
	return value != "" && len(value) <= 253 && !strings.ContainsAny(value, "/@?# \\:\t\n\r") || net.ParseIP(value) != nil
}
func (s *Service) seal(ctx context.Context, tx repository.AccountsTx, id, ownerType, ownerID, purpose string, body any) error {
	ring, err := vault.Load(s.deps.KeyringFile)
	if err != nil {
		return contract.ErrVault
	}
	record := entity.Secret{ID: id, OwnerType: ownerType, OwnerID: ownerID, Purpose: purpose, Revision: 1, UpdatedAt: clock.Now()}
	// Serialized within the account/vault lock. Existing records preserve their
	// immutable owner binding and advance their revision.
	existing, err := tx.Secret(ctx, id)
	if err != nil && !errors.Is(err, contract.ErrNotFound) {
		return err
	}
	var expected int64
	if err == nil {
		record = existing
		expected = existing.Revision
		record.Revision++
	}
	if record.OwnerType != ownerType || record.OwnerID != ownerID || record.Purpose != purpose {
		return contract.ErrVault
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return contract.ErrInvalid
	}
	encrypted, err := ring.Seal(record.Binding(), raw)
	if err != nil {
		return contract.ErrVault
	}
	record.Set(encrypted)
	record.UpdatedAt = clock.Now()
	return tx.SaveSecret(ctx, record, expected)
}
func (s *Service) SaveProxy(ctx context.Context, actorID int64, id string, req contract.ProxyRequest, meta auth.RequestMeta) (contract.Proxy, error) {
	actor, err := s.actor(ctx, actorID, true)
	if err != nil {
		return contract.Proxy{}, err
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Host = strings.TrimSpace(req.Host)
	if !nameOK(req.Name) || !hostOK(req.Host) || req.Port < 1 || req.Port > 65535 {
		return contract.Proxy{}, contract.Invalid("请填写有效的代理名称、主机和端口")
	}
	if req.Auth != nil && (req.Auth.Username == "" || req.Auth.Password == "" || len(req.Auth.Username) > 255 || len(req.Auth.Password) > 255 || req.ClearAuth) {
		return contract.Proxy{}, contract.Invalid("代理认证需要完整的用户名与密码")
	}
	var record entity.Proxy
	err = s.deps.Store.InAccountsTx(ctx, func(tx repository.AccountsTx) error {
		if err := tx.LockAccounts(ctx); err != nil {
			return err
		}
		expected := int64(0)
		now := clock.Now()
		action := "accounts.proxy.created"
		if id == "" {
			record = entity.Proxy{ID: uuid.NewString(), Active: true, Version: 1, CreatedAt: now}
		} else {
			if !validID(id) || req.Active == nil {
				return contract.ErrInvalid
			}
			var err error
			record, err = tx.Proxy(ctx, id)
			if err != nil {
				return err
			}
			if record.Version != req.Version {
				return contract.ErrConflict
			}
			expected = record.Version
			record.Version++
			record.Active = *req.Active
			action = "accounts.proxy.updated"
		}
		record.Name = req.Name
		record.Host = req.Host
		if expected == 0 && req.Active != nil {
			record.Active = *req.Active
		}
		record.Port = req.Port
		record.UpdatedAt = now
		oldSecret := record.SecretID
		if req.ClearAuth {
			record.SecretID = nil
		}
		if req.Auth != nil {
			secretID := uuid.NewString()
			if record.SecretID != nil {
				secretID = *record.SecretID
			}
			if err := s.seal(ctx, tx, secretID, "proxy", record.ID, "proxy_auth", req.Auth); err != nil {
				return err
			}
			record.SecretID = &secretID
		}
		if err := tx.SaveProxy(ctx, record, expected); err != nil {
			return err
		}
		if req.ClearAuth && oldSecret != nil {
			if err := tx.DeleteSecret(ctx, *oldSecret); err != nil {
				return err
			}
		}
		return tx.AppendAudit(ctx, audit(meta, actor, action, "proxy", record.ID, map[string]string{"version": strconv.FormatInt(record.Version, 10)}))
	})
	count, _ := s.deps.Store.ProxyUsage(ctx, record.ID)
	return proxyView(record, count), err
}
func credentialsOK(kind string, value *contract.Credentials) bool {
	if value == nil {
		return false
	}
	if kind == "assume_role" {
		return value.AccessKeyID == "" && value.SecretAccessKey == "" && value.SessionToken == "" && value.ExpiresAt == nil && len(value.ExternalID) <= 1224
	}
	if len(value.AccessKeyID) < 16 || len(value.AccessKeyID) > 128 || len(value.SecretAccessKey) < 16 || len(value.SecretAccessKey) > 128 || value.ExternalID != "" {
		return false
	}
	if strings.IndexFunc(value.AccessKeyID, unicode.IsSpace) >= 0 || strings.IndexFunc(value.SecretAccessKey, unicode.IsSpace) >= 0 {
		return false
	}
	if kind == "access_key" {
		return value.SessionToken == "" && value.ExpiresAt == nil
	}
	return kind == "temporary" && len(value.SessionToken) > 0 && len(value.SessionToken) <= 16384 && value.ExpiresAt != nil && value.ExpiresAt.After(clock.Now())
}
func (s *Service) Connection(ctx context.Context, actorID int64, id string) (contract.Connection, error) {
	if _, err := s.actor(ctx, actorID, true); err != nil {
		return contract.Connection{}, err
	}
	if !validID(id) {
		return contract.Connection{}, contract.ErrInvalid
	}
	value, err := s.deps.Store.Connection(ctx, id)
	if err == nil {
		value = s.currentStatus(ctx, value)
	}
	return connectionView(value, true), err
}
func (s *Service) SaveConnection(ctx context.Context, actorID int64, accountID, id string, req contract.ConnectionRequest, meta auth.RequestMeta) (contract.Connection, error) {
	actor, err := s.actor(ctx, actorID, true)
	if err != nil {
		return contract.Connection{}, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validID(accountID) || !nameOK(req.Name) || (req.AuthType != "access_key" && req.AuthType != "temporary" && req.AuthType != "assume_role") {
		return contract.Connection{}, contract.ErrInvalid
	}
	if req.RouteMode != "direct" && req.RouteMode != "socks5" {
		return contract.Connection{}, contract.Invalid("请选择明确的直连或 SOCKS5 出口")
	}
	if req.RouteMode == "direct" && req.ProxyID != nil || req.RouteMode == "socks5" && (req.ProxyID == nil || !validID(*req.ProxyID)) {
		return contract.Connection{}, contract.Invalid("代理引用与出口模式不匹配")
	}
	if req.Credentials != nil && !credentialsOK(req.AuthType, req.Credentials) {
		return contract.Connection{}, contract.Invalid("凭据不完整、已过期或与认证类型不匹配")
	}
	var record entity.Connection
	err = s.deps.Store.InAccountsTx(ctx, func(tx repository.AccountsTx) error {
		if err := tx.LockAccounts(ctx); err != nil {
			return err
		}
		account, err := tx.Account(ctx, accountID)
		if err != nil {
			return err
		}
		if account.DeletedAt != nil {
			return contract.ErrNotFound
		}
		if !regionOK(account.Partition, req.Region) {
			return contract.Invalid("区域与 AWS 分区不匹配")
		}
		if req.ProxyID != nil {
			if _, err := tx.Proxy(ctx, *req.ProxyID); err != nil {
				return err
			}
		}
		if req.AuthType == "assume_role" {
			role, err := arn.Parse(req.RoleARN)
			if err != nil || role.Partition != account.Partition || role.AccountID != account.AWSAccountID || role.Service != "iam" || !strings.HasPrefix(role.Resource, "role/") ||
				req.SourceConnectionID == nil || !validID(*req.SourceConnectionID) {
				return contract.Invalid("角色 ARN 或来源连接不正确")
			}
			source, err := tx.Connection(ctx, *req.SourceConnectionID)
			if err != nil {
				return err
			}
			sourceAccount, err := tx.Account(ctx, source.AccountID)
			if err != nil {
				return err
			}
			if sourceAccount.Partition != account.Partition {
				return contract.Invalid("角色来源必须处于同一 AWS 分区")
			}
		} else if req.SourceConnectionID != nil || req.RoleARN != "" {
			return contract.ErrInvalid
		}
		expected := int64(0)
		now := clock.Now()
		action := "accounts.connection.created"
		if id == "" {
			if req.Credentials == nil {
				return contract.Invalid("新连接需要提供凭据或角色参数")
			}
			record = entity.Connection{ID: uuid.NewString(), AccountID: accountID, SecretID: uuid.NewString(), Active: true, Version: 1, CreatedAt: now}
		} else {
			if !validID(id) || req.Active == nil {
				return contract.ErrInvalid
			}
			record, err = tx.Connection(ctx, id)
			if err != nil {
				return err
			}
			if record.AccountID != accountID {
				return contract.ErrNotFound
			}
			if record.Version != req.Version {
				return contract.ErrConflict
			}
			if record.AuthType != req.AuthType && req.Credentials == nil {
				return contract.Invalid("更改认证类型时必须重新提供凭据")
			}
			expected = record.Version
			record.Version++
			record.Active = *req.Active
			action = "accounts.connection.updated"
		}
		record.Name = req.Name
		record.AuthType = req.AuthType
		if expected == 0 && req.Active != nil {
			record.Active = *req.Active
		}
		record.Region = req.Region
		record.RouteMode = req.RouteMode
		record.ProxyID = req.ProxyID
		record.SourceConnectionID = req.SourceConnectionID
		record.RoleARN = req.RoleARN
		record.UpdatedAt = now
		record.VerificationStatus = "pending"
		record.VerificationStage = ""
		record.VerificationCode = ""
		record.VerificationFingerprint = ""
		record.PrincipalARN = ""
		record.VerifiedAt = nil
		if req.Credentials != nil {
			if err := s.seal(ctx, tx, record.SecretID, "connection", record.ID, "aws_credentials", req.Credentials); err != nil {
				return err
			}
			record.CredentialExpiresAt = req.Credentials.ExpiresAt
			record.AccessKeySuffix = ""
			if key := req.Credentials.AccessKeyID; len(key) >= 4 {
				record.AccessKeySuffix = key[len(key)-4:]
			}
		}
		if err := tx.SaveConnection(ctx, record, expected); err != nil {
			return err
		}
		if _, err := tx.Graph(ctx, record.ID); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit(meta, actor, action, "connection", record.ID, map[string]string{"version": strconv.FormatInt(record.Version, 10)}))
	})
	return connectionView(record, true), err
}
