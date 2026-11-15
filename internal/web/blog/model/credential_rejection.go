package model

import (
	"strconv"

	"github.com/Laisky/errors/v2"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// CredentialRejectionError is an ErrInvalidCredentials for an account the user
// store found. It carries an opaque, stable identity of that account so the
// adaptive-authentication tracker can count failures per account whatever
// spelling (username, email, letter-case, accent or width variant) the request
// used. Its message is exactly ErrInvalidCredentials, so the identity never
// reaches clients or logs through the error text.
type CredentialRejectionError struct {
	// Identity is the account's risk identity (see User.RiskIdentity).
	Identity string
}

// Error returns the ErrInvalidCredentials message.
func (e *CredentialRejectionError) Error() string {
	return ErrInvalidCredentials.Error()
}

// Is reports that a CredentialRejectionError matches ErrInvalidCredentials.
func (e *CredentialRejectionError) Is(target error) bool {
	return target == ErrInvalidCredentials //nolint:errorlint // sentinel identity comparison inside an Is method
}

// NewCredentialRejection returns an ErrInvalidCredentials carrying identity.
// An empty identity yields the plain sentinel.
func NewCredentialRejection(identity string) error {
	if identity == "" {
		return errors.WithStack(ErrInvalidCredentials)
	}
	return errors.WithStack(&CredentialRejectionError{Identity: identity})
}

// RejectedIdentity returns the account identity carried by a
// CredentialRejectionError anywhere in err's chain, or "".
func RejectedIdentity(err error) string {
	var rejection *CredentialRejectionError
	if errors.As(err, &rejection) && rejection != nil {
		return rejection.Identity
	}
	return ""
}

// MongoRiskIdentity returns the risk identity of a MongoDB-stored account.
func MongoRiskIdentity(id primitive.ObjectID) string {
	if id.IsZero() {
		return ""
	}
	return "mongo:" + id.Hex()
}

// OneAPIRiskIdentity returns the risk identity of a OneAPI-stored account.
func OneAPIRiskIdentity(id int) string {
	if id <= 0 {
		return ""
	}
	return "oneapi:" + strconv.Itoa(id)
}

// RiskIdentity returns the stable identity under which the
// adaptive-authentication tracker counts failures for this account: the OneAPI
// user ID when the account lives in OneAPI, otherwise the MongoDB document ID.
// It returns "" for a nil or unsaved user.
func (u *User) RiskIdentity() string {
	if u == nil {
		return ""
	}
	if identity := OneAPIRiskIdentity(u.OneAPIID); identity != "" {
		return identity
	}
	return MongoRiskIdentity(u.ID)
}
