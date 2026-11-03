package entity

import (
	"github.com/herta-network/aws-panel/internal/auth/vault"
	"time"
)

type Account struct {
	ID                   string
	Partition            string
	AWSAccountID         string
	Name                 string
	Active               bool
	Version              int64
	CreatedAt, UpdatedAt time.Time
	DeletedAt            *time.Time
}
type Proxy struct {
	ID, Name, Host       string
	Port                 int
	SecretID             *string
	Active               bool
	Version              int64
	CreatedAt, UpdatedAt time.Time
}
type Connection struct {
	ID, AccountID, Name, AuthType, Region, RouteMode                                               string
	ProxyID, SourceConnectionID                                                                    *string
	RoleARN, SecretID, AccessKeySuffix                                                             string
	CredentialExpiresAt                                                                            *time.Time
	Active                                                                                         bool
	Version                                                                                        int64
	VerificationStatus, VerificationStage, VerificationCode, VerificationFingerprint, PrincipalARN string
	VerifiedAt                                                                                     *time.Time
	CreatedAt, UpdatedAt                                                                           time.Time
}
type Secret struct {
	ID, OwnerType, OwnerID, Purpose string
	Format                          int
	KeyID                           string
	Nonce                           []byte `json:"-"`
	Ciphertext                      []byte `json:"-"`
	Revision                        int64
	UpdatedAt                       time.Time
}

func (s Secret) Binding() vault.Binding {
	return vault.Binding{ID: s.ID, OwnerType: s.OwnerType, OwnerID: s.OwnerID, Purpose: s.Purpose}
}
func (s Secret) Encrypted() vault.Encrypted {
	return vault.Encrypted{Format: s.Format, KeyID: s.KeyID, Nonce: s.Nonce, Ciphertext: s.Ciphertext}
}
func (s *Secret) Set(value vault.Encrypted) {
	s.Format = value.Format
	s.KeyID = value.KeyID
	s.Nonce = value.Nonce
	s.Ciphertext = value.Ciphertext
}

type Node struct {
	Account     Account
	Connection  Connection
	Secret      Secret
	Proxy       *Proxy
	ProxySecret *Secret
}
