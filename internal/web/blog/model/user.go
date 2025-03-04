package model

import (
	"time"

	gutils "github.com/Laisky/go-utils/v5"
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
	// Status user status
	Status UserStatus `bson:"status" json:"status"`
	// ActiveToken token to active user
	ActiveToken string `bson:"active_token" json:"active_token"`
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
