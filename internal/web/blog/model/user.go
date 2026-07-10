package model

import (
	"encoding/binary"
	"time"

	gutils "github.com/Laisky/go-utils/v6"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var oneAPIObjectIDPrefix = [4]byte{'o', 'n', 'e', 'a'}

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
	ID primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	// OneAPIID is the internal numeric identifier from OneAPI's users table.
	OneAPIID int `bson:"-" json:"-"`
	// OneAPIUsername is the unique login name stored by OneAPI.
	OneAPIUsername string `bson:"-" json:"-"`
	// Role is the OneAPI authorization role.
	Role int `bson:"-" json:"-"`
	// UID is the external stable user identifier exposed to clients and tokens.
	UID string `bson:"uid,omitempty" json:"uid"`
	// ModifiedAt last modified time
	ModifiedAt time.Time `bson:"post_modified_gmt" json:"modified_at"`
	// Username display name
	Username string `bson:"username" json:"username"`
	// Account login account, should be email
	Account string `bson:"account" json:"account"`
	// Password is the OneAPI-compatible bcrypt password hash.
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
	// AttestationType is the WebAuthn attestation statement format.
	AttestationType string `bson:"attestation_type,omitempty" json:"-"`
	// AAGUID is the base64url-encoded authenticator model identifier.
	AAGUID string `bson:"aaguid,omitempty" json:"-"`
	// BackupEligible reports whether the credential supports cloud backup.
	BackupEligible bool `bson:"backup_eligible,omitempty" json:"-"`
	// BackupState reports whether the credential is currently backed up.
	BackupState bool `bson:"backup_state,omitempty" json:"-"`
	// Transport contains comma-separated WebAuthn authenticator transports.
	Transport string `bson:"transport,omitempty" json:"-"`
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

// GetID returns the external stable user UID.
// It accepts no parameters and returns the user UID string.
func (u *User) GetID() string {
	return u.UID
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
	if u.OneAPIID > 0 {
		return u.Role >= 10
	}
	return u.Account == "ppcelery@gmail.com"
}

// SyntheticObjectID maps a positive OneAPI user ID to a stable MongoDB
// ObjectID-shaped value. Blog posts keep ObjectID authorship fields, while the
// authoritative user record lives in OneAPI.
func SyntheticObjectID(oneAPIID int) primitive.ObjectID {
	if oneAPIID <= 0 {
		return primitive.NilObjectID
	}

	var objectID primitive.ObjectID
	copy(objectID[:4], oneAPIObjectIDPrefix[:])
	binary.BigEndian.PutUint64(objectID[4:], uint64(oneAPIID))
	return objectID
}

// OneAPIIDFromSyntheticObjectID decodes an ObjectID created by
// SyntheticObjectID. The boolean result is false for legacy Mongo ObjectIDs or
// values that cannot be represented as a positive int.
func OneAPIIDFromSyntheticObjectID(objectID primitive.ObjectID) (int, bool) {
	if [4]byte(objectID[:4]) != oneAPIObjectIDPrefix {
		return 0, false
	}

	rawID := binary.BigEndian.Uint64(objectID[4:])
	maxInt := uint64(^uint(0) >> 1)
	if rawID > maxInt {
		return 0, false
	}
	decoded := int(rawID) //nolint:gosec // Bound checked against the platform int maximum above.
	if decoded <= 0 || uint64(decoded) != rawID {
		return 0, false
	}
	return decoded, true
}

// NewUser creates a user model with internal and external identifiers.
// It accepts no parameters and returns a new pending user.
func NewUser() *User {
	return &User{
		ID:         primitive.NewObjectID(),
		UID:        gutils.UUID7(),
		ModifiedAt: gutils.Clock.GetUTCNow(),
		Status:     UserStatusPending,
	}
}
