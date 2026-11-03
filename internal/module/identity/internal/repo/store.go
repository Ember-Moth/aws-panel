package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/identity/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/identity/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db    *gorm.DB
	audit kernel.AuditFactory
}

func New(connection kernel.Connection, audit kernel.AuditFactory) *Store {
	return &Store{db: connection.DB, audit: audit}
}

func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contract.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return contract.ErrConflict
	}
	return contract.ErrUnavailable
}

func (s *Store) MemberByUsername(ctx context.Context, name string) (entity.Member, error) {
	var member entity.Member
	err := s.db.WithContext(ctx).Table("members").Where("username = ?", name).Take(&member).Error
	return member, translate(err)
}
func (s *Store) MemberByID(ctx context.Context, id int64) (entity.Member, error) {
	var member entity.Member
	err := s.db.WithContext(ctx).Table("members").Where("id = ?", id).Take(&member).Error
	return member, translate(err)
}
func (s *Store) Session(ctx context.Context, hash string, now time.Time) (entity.Session, entity.Member, error) {
	var record struct {
		entity.Member
		TokenHash        string
		SessionCreatedAt time.Time
		ExpiresAt        time.Time
	}
	err := s.db.WithContext(ctx).Table("sessions AS s").
		Select("m.*, s.token_hash, s.created_at AS session_created_at, s.expires_at").
		Joins("JOIN members AS m ON m.id = s.member_id").
		Where("s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ? AND m.active = true", hash, now).
		Take(&record).Error
	if err != nil {
		return entity.Session{}, entity.Member{}, translate(err)
	}
	return entity.Session{TokenHash: record.TokenHash, MemberID: record.ID, CreatedAt: record.SessionCreatedAt, ExpiresAt: record.ExpiresAt}, record.Member, nil
}
func (s *Store) ListMembers(ctx context.Context, page, size int) ([]entity.Member, int64, error) {
	var total int64
	query := s.db.WithContext(ctx).Table("members")
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, translate(err)
	}
	var members []entity.Member
	err := query.Select("id, username, display_name, role, active, version, created_at, updated_at").
		Order("id ASC").Offset((page - 1) * size).Limit(size).Find(&members).Error
	return members, total, translate(err)
}
func (s *Store) InIdentityTx(ctx context.Context, fn func(repository.IdentityTx) error) error {
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if s.audit == nil {
			return contract.ErrUnavailable
		}
		return fn(&transaction{db: db, audit: s.audit(kernel.Connection{DB: db})})
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

type transaction struct {
	db    *gorm.DB
	audit kernel.AuditAppender
}

func (s *Store) AccountGrants(ctx context.Context, id int64) ([]entity.AccountGrant, error) {
	values := []entity.AccountGrant{}
	err := s.db.WithContext(ctx).Table("account_grants").Where("member_id = ?", id).Order("account_id").Find(&values).Error
	return values, translate(err)
}
func (tx *transaction) SetAccountGrant(ctx context.Context, value entity.AccountGrant) error {
	if value.Access == "none" {
		return translate(tx.db.WithContext(ctx).Exec("DELETE FROM account_grants WHERE member_id = ? AND account_id = ?", value.MemberID, value.AccountID).Error)
	}
	return translate(tx.db.WithContext(ctx).Exec("INSERT INTO account_grants (member_id,account_id,access,updated_at) VALUES (?,?,?,?) ON CONFLICT (member_id,account_id) DO UPDATE SET access=EXCLUDED.access, updated_at=EXCLUDED.updated_at",
		value.MemberID, value.AccountID, value.Access, value.UpdatedAt).Error)
}

func (tx *transaction) LockAdministration(ctx context.Context) error {
	return translate(tx.db.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(?)", int64(92842001)).Error)
}
func (tx *transaction) MemberForUpdate(ctx context.Context, id int64) (entity.Member, error) {
	var member entity.Member
	err := tx.db.WithContext(ctx).Table("members").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&member).Error
	return member, translate(err)
}
func (tx *transaction) MemberCount(ctx context.Context) (int64, error) {
	var count int64
	err := tx.db.WithContext(ctx).Table("members").Count(&count).Error
	return count, translate(err)
}
func (tx *transaction) ActiveAdminCount(ctx context.Context) (int64, error) {
	var count int64
	err := tx.db.WithContext(ctx).Table("members").Where("role = ? AND active = true", contract.RoleAdmin).Count(&count).Error
	return count, translate(err)
}
func (tx *transaction) CreateMember(ctx context.Context, member *entity.Member) error {
	return translate(tx.db.WithContext(ctx).Table("members").Create(member).Error)
}
func (tx *transaction) UpdateMember(ctx context.Context, member entity.Member) error {
	result := tx.db.WithContext(ctx).Table("members").Where("id = ? AND version = ?", member.ID, member.Version-1).
		Updates(map[string]any{"display_name": member.DisplayName, "password_hash": member.PasswordHash, "role": member.Role,
			"active": member.Active, "version": member.Version, "updated_at": member.UpdatedAt})
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected != 1 {
		return contract.ErrConflict
	}
	return nil
}
func (tx *transaction) CreateSession(ctx context.Context, session entity.Session) error {
	return translate(tx.db.WithContext(ctx).Table("sessions").Create(&session).Error)
}
func (tx *transaction) RevokeSession(ctx context.Context, hash string, now time.Time) error {
	return translate(tx.db.WithContext(ctx).Table("sessions").Where("token_hash = ? AND revoked_at IS NULL", hash).Update("revoked_at", now).Error)
}
func (tx *transaction) RevokeMemberSessions(ctx context.Context, id int64, now time.Time) error {
	return translate(tx.db.WithContext(ctx).Table("sessions").Where("member_id = ? AND revoked_at IS NULL", id).Update("revoked_at", now).Error)
}
func (tx *transaction) AppendAudit(ctx context.Context, event kernel.AuditEntry) error {
	if tx.audit == nil {
		return contract.ErrUnavailable
	}
	if err := tx.audit.AppendAudit(ctx, event); err != nil {
		return contract.ErrUnavailable
	}
	return nil
}
