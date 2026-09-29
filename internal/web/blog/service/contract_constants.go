package service

// Shared wire-format and storage identifiers keep these contracts consistent.
const (
	mongoElemMatch       = "$elemMatch"
	mongoSet             = "$set"
	fieldDocumentID      = "_id"
	fieldAccount         = "account"
	fieldCategory        = "category"
	fieldCreatedAt       = "created_at"
	fieldIsApproved      = "is_approved"
	fieldLikes           = "likes"
	fieldOidcIdentities  = "oidc_identities"
	fieldPasskeys        = "passkeys"
	fieldPasskeysId      = "passkeys.id"
	fieldPostId          = "post_id"
	fieldPostModifiedGmt = "post_modified_gmt"
	fieldPostName        = "post_name"
	fieldTotpEnabled     = "totp_enabled"
	fieldUid             = "uid"
)
