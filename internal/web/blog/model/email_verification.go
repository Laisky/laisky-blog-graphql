package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	// EmailVerificationPurposeRegister is used for new account registration.
	EmailVerificationPurposeRegister = "register"
	// EmailVerificationPurposeLogin is used for passwordless email-code login.
	EmailVerificationPurposeLogin = "login"
)

// EmailVerificationCode stores a hashed one-time email verification code.
type EmailVerificationCode struct {
	// ID is the storage identifier for this verification challenge.
	ID primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	// Account is the normalized email account this code was issued for.
	Account string `bson:"account" json:"account"`
	// Purpose identifies whether this code is for registration or login.
	Purpose string `bson:"purpose" json:"purpose"`
	// CodeHash is an HMAC-SHA256 hash of the code and metadata.
	CodeHash string `bson:"code_hash" json:"-"`
	// CreatedAt records when this code was issued.
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	// ExpiresAt records when this code becomes invalid and TTL-prunable.
	ExpiresAt time.Time `bson:"expires_at" json:"expires_at"`
}
