package entity

import "time"

type Member struct {
	ID           int64
	Username     string
	DisplayName  string
	PasswordHash string `json:"-"`
	Role         string
	Active       bool
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Session struct {
	TokenHash string `json:"-"`
	MemberID  int64
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
}

type AccountGrant struct {
	MemberID          int64
	AccountID, Access string
	UpdatedAt         time.Time
}
