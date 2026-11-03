package repo

import (
	"context"
	"database/sql"
	"errors"

	"github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/module/accounts/entity"
	"github.com/herta-network/aws-panel/internal/repository"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

type Store struct {
	db    *gorm.DB
	audit kernel.AuditFactory
	guard kernel.ReferenceGuardFactory
}

func New(connection kernel.Connection, audit kernel.AuditFactory, guards ...kernel.ReferenceGuardFactory) *Store {
	s := &Store{db: connection.DB, audit: audit}
	if len(guards) > 0 {
		s.guard = guards[0]
	}
	return s
}
func convert(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contract.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23503") {
		return contract.ErrConflict
	}
	return contract.ErrUnavailable
}
func (s *Store) Account(ctx context.Context, id string) (entity.Account, error) {
	var v entity.Account
	err := s.db.WithContext(ctx).Table("aws_accounts a").Select("a.*,p.name AS default_proxy_name").Joins("LEFT JOIN proxy_profiles p ON p.id=a.default_proxy_id").Where("a.id = ?", id).Take(&v).Error
	return v, convert(err)
}
func (s *Store) Accounts(ctx context.Context, ids []string, all bool) ([]entity.Account, error) {
	values := []entity.Account{}
	if !all && len(ids) == 0 {
		return values, nil
	}
	q := s.db.WithContext(ctx).Table("aws_accounts a").Select("a.*,p.name AS default_proxy_name").Joins("LEFT JOIN proxy_profiles p ON p.id=a.default_proxy_id").Where("a.deleted_at IS NULL")
	if !all {
		q = q.Where("a.id IN ? AND a.active = true", ids)
	}
	err := q.Order("a.created_at DESC, a.id").Find(&values).Error
	return values, convert(err)
}
func (s *Store) Proxy(ctx context.Context, id string) (entity.Proxy, error) {
	var v entity.Proxy
	err := s.db.WithContext(ctx).Table("proxy_profiles").Where("id = ?", id).Take(&v).Error
	return v, convert(err)
}
func (s *Store) Proxies(ctx context.Context) ([]entity.Proxy, error) {
	v := []entity.Proxy{}
	err := s.db.WithContext(ctx).Table("proxy_profiles").Order("created_at DESC, id").Find(&v).Error
	return v, convert(err)
}
func (s *Store) ProxyUsage(ctx context.Context, id string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Table("connections").Where("proxy_id = ?", id).Count(&count).Error
	return count, convert(err)
}
func (s *Store) ProxyAccountUsage(ctx context.Context, id string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Table("aws_accounts").Where("default_proxy_id=? AND deleted_at IS NULL", id).Count(&count).Error
	return count, convert(err)
}
func (s *Store) ProxyImpact(ctx context.Context, id string) ([]entity.Connection, error) {
	values := []entity.Connection{}
	err := s.db.WithContext(ctx).Raw(`WITH RECURSIVE affected AS (
		SELECT c.* FROM connections c JOIN aws_accounts a ON a.id=c.account_id WHERE c.proxy_id = ? OR (c.route_mode='inherit' AND a.default_proxy_id=? AND a.deleted_at IS NULL)
		UNION SELECT child.* FROM connections child JOIN affected parent ON child.source_connection_id = parent.id
	) SELECT * FROM affected ORDER BY account_id, name, id`, id, id).Scan(&values).Error
	return values, convert(err)
}
func (s *Store) Connection(ctx context.Context, id string) (entity.Connection, error) {
	var v entity.Connection
	err := s.db.WithContext(ctx).Table("connections").Where("id = ?", id).Take(&v).Error
	return v, convert(err)
}
func (s *Store) Connections(ctx context.Context, id string) ([]entity.Connection, error) {
	v := []entity.Connection{}
	err := s.db.WithContext(ctx).Table("connections").Where("account_id = ?", id).Order("created_at, id").Find(&v).Error
	return v, convert(err)
}
func (s *Store) Secret(ctx context.Context, id string) (entity.Secret, error) {
	var v entity.Secret
	err := s.db.WithContext(ctx).Table("secrets").Where("id = ?", id).Take(&v).Error
	return v, convert(err)
}
func (s *Store) Secrets(ctx context.Context) ([]entity.Secret, error) {
	v := []entity.Secret{}
	err := s.db.WithContext(ctx).Table("secrets").Order("id").Find(&v).Error
	return v, convert(err)
}
func (s *Store) Graph(ctx context.Context, id string) ([]entity.Node, error) {
	var nodes []entity.Node
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		store := &Store{db: tx, audit: s.audit}
		seen := map[string]bool{}
		for id != "" {
			if seen[id] || len(nodes) >= 8 {
				return contract.Invalid("角色来源形成循环或超过八层")
			}
			seen[id] = true
			connection, err := store.Connection(ctx, id)
			if err != nil {
				return err
			}
			account, err := store.Account(ctx, connection.AccountID)
			if err != nil {
				return err
			}
			secret, err := store.Secret(ctx, connection.SecretID)
			if err != nil {
				return err
			}
			node := entity.Node{Connection: connection, Account: account, Secret: secret}
			proxyID := connection.ProxyID
			mode := connection.RouteMode
			if mode == "inherit" {
				proxyID = account.DefaultProxyID
				mode = account.DefaultRouteMode
			}
			if mode != "direct" && mode != "socks5" && mode != "http" {
				return contract.ErrInvalid
			}
			if (mode == "direct") != (proxyID == nil) {
				return contract.ErrInvalid
			}
			if proxyID != nil {
				proxy, err := store.Proxy(ctx, *proxyID)
				if err != nil {
					return err
				}
				if proxy.Protocol != mode {
					return contract.Invalid("代理协议与出口不匹配")
				}
				node.Proxy = &proxy
				if proxy.SecretID != nil {
					auth, err := store.Secret(ctx, *proxy.SecretID)
					if err != nil {
						return err
					}
					node.ProxySecret = &auth
				}
			}
			nodes = append(nodes, node)
			id = ""
			if connection.SourceConnectionID != nil {
				id = *connection.SourceConnectionID
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return nodes, err
}
func (s *Store) InAccountsTx(ctx context.Context, fn func(repository.AccountsTx) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return fn(&Store{db: tx, audit: s.audit, guard: s.guard}) }, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}
func (s *Store) LockAccounts(ctx context.Context) error {
	return convert(s.db.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(?)", kernel.CloudConfigurationLock).Error)
}
func (s *Store) CheckRemoval(ctx context.Context, kind, id string) error {
	if s.guard == nil {
		return nil
	}
	used, err := s.guard(kernel.Connection{DB: s.db}).InUse(ctx, kind, id)
	if err != nil {
		return contract.ErrUnavailable
	}
	if used {
		return contract.Invalid("此配置仍被未完成任务引用，请先处理任务结果")
	}
	return nil
}
func (s *Store) AppendAudit(ctx context.Context, event kernel.AuditEntry) error {
	if s.audit == nil {
		return contract.ErrUnavailable
	}
	if err := s.audit(kernel.Connection{DB: s.db}).AppendAudit(ctx, event); err != nil {
		return contract.ErrUnavailable
	}
	return nil
}
func (s *Store) save(ctx context.Context, table, id string, value any, version int64) error {
	if version == 0 {
		return convert(s.db.WithContext(ctx).Table(table).Create(value).Error)
	}
	result := s.db.WithContext(ctx).Table(table).Where("id = ? AND version = ?", id, version).Select("*").Updates(value)
	if result.Error != nil {
		return convert(result.Error)
	}
	if result.RowsAffected != 1 {
		return contract.ErrConflict
	}
	return nil
}
func (s *Store) SaveAccount(ctx context.Context, v entity.Account, version int64) error {
	return s.save(ctx, "aws_accounts", v.ID, &v, version)
}
func (s *Store) SaveProxy(ctx context.Context, v entity.Proxy, version int64) error {
	return s.save(ctx, "proxy_profiles", v.ID, &v, version)
}
func (s *Store) SaveConnection(ctx context.Context, v entity.Connection, version int64) error {
	return s.save(ctx, "connections", v.ID, &v, version)
}
func (s *Store) SaveSecret(ctx context.Context, v entity.Secret, revision int64) error {
	if revision == 0 {
		return convert(s.db.WithContext(ctx).Table("secrets").Create(&v).Error)
	}
	result := s.db.WithContext(ctx).Table("secrets").Where("id = ? AND revision = ?", v.ID, revision).Select("*").Updates(&v)
	if result.Error != nil {
		return convert(result.Error)
	}
	if result.RowsAffected != 1 {
		return contract.ErrConflict
	}
	return nil
}
func (s *Store) DeleteSecret(ctx context.Context, id string) error {
	return convert(s.db.WithContext(ctx).Exec("DELETE FROM secrets WHERE id = ?", id).Error)
}
func (s *Store) SaveVerification(ctx context.Context, v entity.Connection) error {
	result := s.db.WithContext(ctx).Table("connections").Where("id = ? AND version = ?", v.ID, v.Version).
		Updates(map[string]any{"verification_status": v.VerificationStatus, "verification_stage": v.VerificationStage,
			"verification_code": v.VerificationCode, "verification_fingerprint": v.VerificationFingerprint, "principal_arn": v.PrincipalARN, "verified_at": v.VerifiedAt})
	if result.Error != nil {
		return convert(result.Error)
	}
	if result.RowsAffected != 1 {
		return contract.ErrStale
	}
	return nil
}

func (s *Store) SourceUsage(ctx context.Context, id string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Table("connections").Where("source_connection_id = ?", id).Count(&count).Error
	return count, convert(err)
}
func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	return convert(s.db.WithContext(ctx).Exec("DELETE FROM connections WHERE id = ?", id).Error)
}
func (s *Store) DeleteProxy(ctx context.Context, id string) error {
	return convert(s.db.WithContext(ctx).Exec("DELETE FROM proxy_profiles WHERE id = ?", id).Error)
}
