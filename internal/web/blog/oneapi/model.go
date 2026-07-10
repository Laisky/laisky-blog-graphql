package oneapi

import "time"

// OneAPI user role levels mirror the authoritative OneAPI model.
const (
	RoleGuestUser  = 0
	RoleCommonUser = 1
	RoleAdminUser  = 10
	RoleRootUser   = 100
)

// OneAPI user status values mirror the authoritative OneAPI model.
const (
	StatusEnabled  = 1
	StatusDisabled = 2
	StatusDeleted  = 3
)

const tokenStatusEnabled = 1

const (
	columnUUID         = "uuid"
	oidcProviderGitHub = "github"
)

// User is the SSO-owned projection of OneAPI's users table. The field and
// column definitions intentionally match the sibling OneAPI model/user.go.
type User struct {
	ID          int    `gorm:"primaryKey;column:id"`
	UUID        string `gorm:"type:char(36);column:uuid"`
	Username    string `gorm:"column:username"`
	Password    string `gorm:"column:password"`
	DisplayName string `gorm:"column:display_name"`
	Role        int    `gorm:"column:role"`
	Status      int    `gorm:"column:status"`
	Email       string `gorm:"column:email"`
	GitHubID    string `gorm:"column:github_id"`
	OIDCID      string `gorm:"column:oidc_id"`
	TOTPSecret  string `gorm:"column:totp_secret"`
	AccessToken string `gorm:"column:access_token"`
	AffCode     string `gorm:"column:aff_code"`
	Quota       int64  `gorm:"column:quota"`
	CreatedAt   int64  `gorm:"column:created_at;autoCreateTime:milli"`
	UpdatedAt   int64  `gorm:"column:updated_at;autoUpdateTime:milli"`
}

// TableName binds User to OneAPI's existing users table.
func (User) TableName() string { return "users" }

type oneAPIToken struct {
	ID             int     `gorm:"primaryKey;column:id"`
	UUID           string  `gorm:"type:char(36);column:uuid"`
	UserID         int     `gorm:"column:user_id"`
	UserUUID       *string `gorm:"type:char(36);column:user_uuid"`
	Key            string  `gorm:"column:key"`
	Status         int     `gorm:"column:status"`
	Name           string  `gorm:"column:name"`
	CreatedTime    int64   `gorm:"column:created_time"`
	AccessedTime   int64   `gorm:"column:accessed_time"`
	ExpiredTime    int64   `gorm:"column:expired_time"`
	RemainQuota    int64   `gorm:"column:remain_quota"`
	UnlimitedQuota bool    `gorm:"column:unlimited_quota"`
	CreatedAt      int64   `gorm:"column:created_at;autoCreateTime:milli"`
	UpdatedAt      int64   `gorm:"column:updated_at;autoUpdateTime:milli"`
}

func (oneAPIToken) TableName() string { return "tokens" }

type oneAPIOption struct {
	Key       string `gorm:"primaryKey;column:key"`
	Value     string `gorm:"column:value"`
	CreatedAt int64  `gorm:"column:created_at;autoCreateTime:milli"`
	UpdatedAt int64  `gorm:"column:updated_at;autoUpdateTime:milli"`
}

func (oneAPIOption) TableName() string { return "options" }

// PasskeyCredential mirrors OneAPI's native passkey_credentials table.
type PasskeyCredential struct {
	ID              int     `gorm:"primaryKey;autoIncrement;column:id"`
	UUID            string  `gorm:"type:char(36);column:uuid"`
	UserID          int     `gorm:"column:user_id"`
	UserUUID        *string `gorm:"type:char(36);column:user_uuid"`
	CredentialName  string  `gorm:"column:credential_name"`
	CredentialID    []byte  `gorm:"column:credential_id"`
	PublicKey       []byte  `gorm:"column:public_key"`
	AttestationType string  `gorm:"column:attestation_type"`
	AAGUID          []byte  `gorm:"column:aaguid"`
	SignCount       uint32  `gorm:"column:sign_count"`
	BackupEligible  bool    `gorm:"column:backup_eligible"`
	BackupState     bool    `gorm:"column:backup_state"`
	Transport       string  `gorm:"column:transport"`
	CreatedAt       int64   `gorm:"column:created_at;autoCreateTime:milli"`
	UpdatedAt       int64   `gorm:"column:updated_at;autoUpdateTime:milli"`
}

// TableName binds PasskeyCredential to OneAPI's native passkey table.
func (PasskeyCredential) TableName() string { return "passkey_credentials" }

// SSOUserLink preserves the public SSO UUID and Mongo-shaped blog author ID
// while OneAPI's numeric user ID remains an internal database key.
type SSOUserLink struct {
	OneAPIUserID   int       `gorm:"column:oneapi_user_id;primaryKey"`
	OneAPIUserUUID string    `gorm:"type:char(36);column:oneapi_user_uuid;uniqueIndex"`
	SSOUID         string    `gorm:"type:char(36);column:sso_uid;uniqueIndex"`
	BlogObjectID   string    `gorm:"type:char(24);column:blog_object_id;uniqueIndex"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

// TableName returns the SSO user-link table name.
func (SSOUserLink) TableName() string { return "sso_user_links" }

// SSOOIDCIdentity serializes provider-subject ownership independently of
// OneAPI's non-unique github_id column.
type SSOOIDCIdentity struct {
	Provider  string    `gorm:"column:provider;primaryKey;uniqueIndex:idx_sso_oidc_provider_user;size:32"`
	Subject   string    `gorm:"column:subject;primaryKey;size:255"`
	UserID    int       `gorm:"column:user_id;index;uniqueIndex:idx_sso_oidc_provider_user"`
	Email     string    `gorm:"column:email;size:254"`
	BoundAt   time.Time `gorm:"column:bound_at"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TableName returns the SSO OIDC identity table name.
func (SSOOIDCIdentity) TableName() string { return "sso_oidc_identities" }

// SSOEmailVerificationCode stores one hashed email challenge per
// account-purpose pair.
type SSOEmailVerificationCode struct {
	ID        string    `gorm:"type:char(36);column:id;primaryKey"`
	Account   string    `gorm:"column:account;uniqueIndex:idx_sso_evc_account_purpose;size:254"`
	Purpose   string    `gorm:"column:purpose;uniqueIndex:idx_sso_evc_account_purpose;size:32"`
	CodeHash  string    `gorm:"column:code_hash;size:64"`
	CreatedAt time.Time `gorm:"column:created_at"`
	ExpiresAt time.Time `gorm:"column:expires_at;index"`
}

// TableName returns the SSO email-code table name.
func (SSOEmailVerificationCode) TableName() string { return "sso_email_verification_codes" }

// SSOTOTPEnrollment stores an unconfirmed TOTP secret. OneAPI's users table is
// updated only after successful confirmation.
type SSOTOTPEnrollment struct {
	UserID    int       `gorm:"column:user_id;primaryKey"`
	Secret    string    `gorm:"column:secret;size:64"`
	CreatedAt time.Time `gorm:"column:created_at"`
	ExpiresAt time.Time `gorm:"column:expires_at;index"`
}

// TableName returns the pending TOTP enrollment table name.
func (SSOTOTPEnrollment) TableName() string { return "sso_totp_enrollments" }
