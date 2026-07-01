package model

import (
	"time"

	gutils "github.com/Laisky/go-utils/v6"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// UserStatus user status
type UserStatus string

const (
	// UserStatusActive active user
	UserStatusActive UserStatus = "active"
	// UserStatusPending wait for email verification
	UserStatusPending UserStatus = "pending"
)

// User blog users
type User struct {
	// ID unique identifier for the user
	ID primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	// ModifiedAt last modified time
	ModifiedAt time.Time `bson:"post_modified_gmt" json:"modified_at"`
	// Username display name
	Username string `bson:"username" json:"username"`
	// Account login account, should be email
	Account string `bson:"account" json:"account"`
	// Password hashed password
	//
	//  `gcrypto.ValidatePasswordHash`
	Password string `bson:"password" json:"password"`
	// TOTPSecret is the secret used to verify time-based one-time passwords.
	TOTPSecret string `bson:"totp_secret,omitempty" json:"-"`
	// TOTPEnabled indicates whether password login must include a TOTP code.
	TOTPEnabled bool `bson:"totp_enabled,omitempty" json:"totp_enabled"`
	// Passkeys stores WebAuthn credentials associated with the user.
	Passkeys []PasskeyCredential `bson:"passkeys,omitempty" json:"-"`
	// OIDCIdentities stores external OIDC identities associated with the user.
	OIDCIdentities []OIDCIdentity `bson:"oidc_identities,omitempty" json:"-"`
	// Status user status
	Status UserStatus `bson:"status" json:"status"`
	// ActiveToken token to active user
	ActiveToken string `bson:"active_token" json:"active_token"`
}

// PasskeyCredential stores a WebAuthn credential registered by the user.
type PasskeyCredential struct {
	// ID is the credential identifier from the authenticator.
	ID string `bson:"id" json:"id"`
	// Name is the user-visible credential label.
	Name string `bson:"name" json:"name"`
	// PublicKey is the credential public key encoded for storage.
	PublicKey string `bson:"public_key" json:"-"`
	// CredentialJSON is the complete WebAuthn credential record encoded as JSON.
	CredentialJSON string `bson:"credential_json,omitempty" json:"-"`
	// SignCount is the authenticator signature counter.
	SignCount uint32 `bson:"sign_count" json:"-"`
	// CreatedAt records when the credential was added.
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

// OIDCIdentity stores an external OIDC identity bound to the user.
type OIDCIdentity struct {
	// Provider is the normalized OIDC provider name, such as "github".
	Provider string `bson:"provider" json:"provider"`
	// Subject is the stable provider subject identifier.
	Subject string `bson:"subject" json:"subject"`
	// Email is the verified provider email when available.
	Email string `bson:"email,omitempty" json:"email,omitempty"`
	// BoundAt records when the identity was associated.
	BoundAt time.Time `bson:"bound_at" json:"bound_at"`
}

// GetID get id
func (u *User) GetID() string {
	return u.ID.Hex()
}

// GetPayload get payload
func (u *User) GetPayload() map[string]interface{} {
	return map[string]interface{}{
		"display_name": u.Username,
		"account":      u.Account,
	}
}

// IsAdmin is admin
func (u *User) IsAdmin() bool {
	return u.Account == "ppcelery@gmail.com"
}

// NewUser create a new user
func NewUser() *User {
	return &User{
		ID:         primitive.NewObjectID(),
		ModifiedAt: gutils.Clock.GetUTCNow(),
		Status:     UserStatusPending,
	}
}
