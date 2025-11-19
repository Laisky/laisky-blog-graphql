package calllog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// Status enumerations for recorded tool calls.
const (
	StatusSuccess = "success"
	StatusError   = "error"
)

// Clock provides the current time in UTC.
type Clock func() time.Time

// Service persists and queries tool invocation call logs.
type Service struct {
	db     *gorm.DB
	logger logSDK.Logger
	clock  Clock
}

// RecordInput captures the information required to persist a tool invocation.
type RecordInput struct {
	ToolName     string
	APIKey       string
	Status       string
	Cost         int
	CostUnit     string
	Duration     time.Duration
	Parameters   map[string]any
	ErrorMessage string
	OccurredAt   time.Time
}

// ListOptions configures the result set returned by List.
type ListOptions struct {
	Page       int
	PageSize   int
	ToolName   string
	UserPrefix string
	APIKeyHash string
	SortField  string
	SortOrder  string
	From       time.Time
	To         time.Time
}

// Entry represents a single record returned from List.
type Entry struct {
	ID             uuid.UUID
	ToolName       string
	APIKeyHash     string
	KeyPrefix      string
	Status         string
	Cost           int
	CostUnit       string
	DurationMillis int64
	Parameters     map[string]any
	ErrorMessage   string
	OccurredAt     time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ListResult packages the results of a List query along with the total count.
type ListResult struct {
	Entries []Entry
	Total   int64
}

const (
	defaultCostUnit    = "quota"
	defaultPage        = 1
	defaultPageSize    = 20
	maxPageSize        = 100
	sortFieldCreatedAt = "created_at"
	sortFieldCost      = "cost"
	sortFieldDuration  = "duration"
)

// NewService constructs a Service backed by the supplied gorm database.
func NewService(db *gorm.DB, logger logSDK.Logger, clock Clock) (*Service, error) {
	if db == nil {
		return nil, errors.New("gorm db is required")
	}
	if logger == nil {
		logger = log.Logger.Named("call_log_service")
	}
	if clock == nil {
		clock = func() time.Time {
			return time.Now().UTC()
		}
	}

	if err := db.AutoMigrate(&Record{}); err != nil {
		return nil, errors.Wrap(err, "auto migrate call_log records")
	}

	return &Service{db: db, logger: logger, clock: clock}, nil
}

// Record stores a tool invocation using the provided input.
func (s *Service) Record(ctx context.Context, input RecordInput) error {
	if s == nil {
		return errors.New("call log service is nil")
	}
	trimmedTool := strings.TrimSpace(input.ToolName)
	if trimmedTool == "" {
		return errors.New("tool name is required")
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = StatusSuccess
	}
	costUnit := input.CostUnit
	if strings.TrimSpace(costUnit) == "" {
		costUnit = defaultCostUnit
	}

	keyHash, keyPrefix := normalizeAPIKey(input.APIKey)
	payload, err := json.Marshal(input.Parameters)
	if err != nil {
		return errors.Wrap(err, "marshal call log parameters")
	}

	occurred := input.OccurredAt
	if occurred.IsZero() {
		occurred = s.clock()
	}

	record := &Record{
		ToolName:       trimmedTool,
		APIKeyHash:     keyHash,
		KeyPrefix:      keyPrefix,
		Status:         status,
		Cost:           input.Cost,
		CostUnit:       costUnit,
		DurationMillis: input.Duration.Milliseconds(),
		Parameters:     datatypes.JSON(payload),
		ErrorMessage:   strings.TrimSpace(input.ErrorMessage),
		OccurredAt:     occurred,
	}

	// Use a detached context to ensure logging completes even if the request is cancelled.
	ctx = context.WithoutCancel(ctx)
	if err := s.db.WithContext(ctx).Create(record).Error; err != nil {
		return errors.Wrap(err, "create call log record")
	}

	s.logger.Debug("recorded call log", zap.String("tool", trimmedTool), zap.String("status", status))
	return nil
}

// List retrieves records that match the provided filters and pagination options.
func (s *Service) List(ctx context.Context, opts ListOptions) (*ListResult, error) {
	if s == nil {
		return nil, errors.New("call log service is nil")
	}

	page := opts.Page
	if page < 1 {
		page = defaultPage
	}
	size := opts.PageSize
	if size <= 0 {
		size = defaultPageSize
	} else if size > maxPageSize {
		size = maxPageSize
	}

	query := s.db.WithContext(ctx).Model(&Record{})

	if opts.APIKeyHash != "" {
		query = query.Where("api_key_hash = ?", opts.APIKeyHash)
	}
	if trimmed := strings.TrimSpace(opts.ToolName); trimmed != "" {
		query = query.Where("tool_name = ?", trimmed)
	}
	if trimmed := strings.TrimSpace(opts.UserPrefix); trimmed != "" {
		like := trimmed + "%"
		query = query.Where("key_prefix LIKE ?", like)
	}
	if !opts.From.IsZero() {
		query = query.Where("occurred_at >= ?", opts.From)
	}
	if !opts.To.IsZero() {
		query = query.Where("occurred_at < ?", opts.To)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errors.Wrap(err, "count call log records")
	}

	orderField := mapSortField(opts.SortField)
	orderDirection := strings.ToUpper(opts.SortOrder)
	if orderDirection != "ASC" {
		orderDirection = "DESC"
	}
	query = query.Order(orderField + " " + orderDirection)

	offset := (page - 1) * size
	query = query.Offset(offset).Limit(size)

	records := make([]Record, 0, size)
	if err := query.Find(&records).Error; err != nil {
		return nil, errors.Wrap(err, "query call log records")
	}

	entries := make([]Entry, 0, len(records))
	for _, record := range records {
		params := map[string]any{}
		if len(record.Parameters) > 0 {
			if err := json.Unmarshal(record.Parameters, &params); err != nil {
				s.logger.Warn("decode call log parameters", zap.Error(err), zap.String("record_id", record.ID.String()))
				params = map[string]any{}
			}
		}

		entries = append(entries, Entry{
			ID:             record.ID,
			ToolName:       record.ToolName,
			APIKeyHash:     record.APIKeyHash,
			KeyPrefix:      record.KeyPrefix,
			Status:         record.Status,
			Cost:           record.Cost,
			CostUnit:       record.CostUnit,
			DurationMillis: record.DurationMillis,
			Parameters:     params,
			ErrorMessage:   record.ErrorMessage,
			OccurredAt:     record.OccurredAt,
			CreatedAt:      record.CreatedAt,
			UpdatedAt:      record.UpdatedAt,
		})
	}

	return &ListResult{Entries: entries, Total: total}, nil
}

func mapSortField(field string) string {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case sortFieldCost:
		return "cost"
	case sortFieldDuration:
		return "duration_millis"
	default:
		return "occurred_at"
	}
}

func normalizeAPIKey(apiKey string) (hash string, prefix string) {
	trimmed := strings.TrimSpace(apiKey)
	if trimmed == "" {
		return "", ""
	}

	hashed := sha256.Sum256([]byte(trimmed))
	hash = hex.EncodeToString(hashed[:])

	const prefixLength = 7
	if len(trimmed) > prefixLength {
		prefix = trimmed[:prefixLength]
	} else {
		prefix = trimmed
	}

	return hash, prefix
}
