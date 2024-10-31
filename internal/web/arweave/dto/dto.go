package dto

type UploadResponse struct {
	FileID string `json:"file_id"`
}

type ArdriveOutput struct {
	Created []createdItem `json:"created"`
}

type createdItem struct {
	Type         string `json:"type"`
	EntityName   string `json:"entityName,omitempty"`
	EntityId     string `json:"entityId,omitempty"`
	DataTxId     string `json:"dataTxId,omitempty"`
	MetadataTxId string `json:"metadataTxId,omitempty"`
}
