package dao

// Shared wire-format and storage identifiers keep these contracts consistent.
const (
	mongoSet         = "$set"
	mongoSetOnInsert = "$setOnInsert"
	fieldDocumentID  = "_id"
	fieldAlertId     = "alert_id"
	fieldCreatedAt   = "created_at"
	fieldModifiedAt  = "modified_at"
	fieldName        = "name"
	fieldTelegramUid = "telegram_uid"
	fieldUid         = "uid"
)
