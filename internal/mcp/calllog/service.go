package calllog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

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
	db     DB
	logger logSDK.Logger
	clock  Clock
}

// DB defines the database capabilities required by the call log service.
type DB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
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
	defaultCostUnit = "quota"
	defaultPage     = 1
	// defaultPageSize sets the fallback page size for list queries.
	defaultPageSize = 20
	// maxPageSize caps the page size for list queries.
	maxPageSize        = 100
	sortFieldCreatedAt = "created_at"
	sortFieldCost      = "cost"
	sortFieldDuration  = "duration"
)

// NewService constructs a Service backed by the supplied PostgreSQL connection.
func NewService(db DB, logger logSDK.Logger, clock Clock) (*Service, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	if logger == nil {
		logger = log.Logger.Named("call_log_service")
	}
	if clock == nil {
		clock = func() time.Time {
			return time.Now().UTC()
		}
	}

	if err := runMigrations(context.Background(), db); err != nil {
		return nil, errors.Wrap(err, "migrate call_log records")
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

	recordID := gutils.UUID7Bytes()
	record := &Record{
		ID:             recordID,
		ToolName:       trimmedTool,
		APIKeyHash:     keyHash,
		KeyPrefix:      keyPrefix,
		Status:         status,
		Cost:           input.Cost,
		CostUnit:       costUnit,
		DurationMillis: input.Duration.Milliseconds(),
		Parameters:     payload,
		ErrorMessage:   strings.TrimSpace(input.ErrorMessage),
		OccurredAt:     occurred,
		CreatedAt:      s.clock(),
		UpdatedAt:      s.clock(),
	}

	// Use a detached context to ensure logging completes even if the request is cancelled.
	ctx = context.WithoutCancel(ctx)
	_, err = s.db.Exec(ctx, `
		INSERT INTO mcp_call_logs (
			id, tool_name, api_key_hash, key_prefix, status, cost, cost_unit,
			duration_millis, parameters, error_message, occurred_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9::jsonb, $10, $11, $12, $13
		)
	`,
		record.ID,
		record.ToolName,
		record.APIKeyHash,
		record.KeyPrefix,
		record.Status,
		record.Cost,
		record.CostUnit,
		record.DurationMillis,
		string(record.Parameters),
		record.ErrorMessage,
		record.OccurredAt,
		record.CreatedAt,
		record.UpdatedAt,
	)
	if err != nil {
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

	toolName, err := sanitizeOptionalText(opts.ToolName, maxToolNameLength, "tool name")
	if err != nil {
		return nil, errors.Wrap(err, "sanitize tool name")
	}
	userPrefix, err := sanitizeOptionalText(opts.UserPrefix, maxUserPrefixLength, "user prefix")
	if err != nil {
		return nil, errors.Wrap(err, "sanitize user prefix")
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

	clauses := make([]string, 0, 6)
	args := make([]any, 0, 8)
	argID := 1
	if opts.APIKeyHash != "" {
		clauses = append(clauses, fmt.Sprintf("api_key_hash = $%d", argID))
		args = append(args, opts.APIKeyHash)
		argID++
	}
	if toolName != "" {
		clauses = append(clauses, fmt.Sprintf("tool_name = $%d", argID))
		args = append(args, toolName)
		argID++
	}
	if userPrefix != "" {
		like := userPrefix + "%"
		clauses = append(clauses, fmt.Sprintf("key_prefix LIKE $%d", argID))
		args = append(args, like)
		argID++
	}
	if !opts.From.IsZero() {
		clauses = append(clauses, fmt.Sprintf("occurred_at >= $%d", argID))
		args = append(args, opts.From)
		argID++
	}
	if !opts.To.IsZero() {
		clauses = append(clauses, fmt.Sprintf("occurred_at < $%d", argID))
		args = append(args, opts.To)
		argID++
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = " WHERE " + strings.Join(clauses, " AND ")
	}

	var total int64
	countSQL := "SELECT COUNT(*) FROM mcp_call_logs" + whereSQL
	if err := s.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, errors.Wrap(err, "count call log records")
	}

	orderField := mapSortField(opts.SortField)
	orderDirection := strings.ToUpper(opts.SortOrder)
	if orderDirection != "ASC" {
		orderDirection = "DESC"
	}
	offset := (page - 1) * size
	listSQL := fmt.Sprintf(`
		SELECT id, tool_name, api_key_hash, key_prefix, status, cost, cost_unit,
			duration_millis, parameters, error_message, occurred_at, created_at, updated_at
		FROM mcp_call_logs
		%s
		ORDER BY %s %s
		OFFSET $%d LIMIT $%d
	`, whereSQL, orderField, orderDirection, argID, argID+1)
	listArgs := append(args, offset, size)
	rows, err := s.db.Query(ctx, listSQL, listArgs...)
	if err != nil {
		return nil, errors.Wrap(err, "query call log records")
	}
	defer rows.Close()

	records := make([]Record, 0, size)
	for rows.Next() {
		var record Record
		if scanErr := rows.Scan(
			&record.ID,
			&record.ToolName,
			&record.APIKeyHash,
			&record.KeyPrefix,
			&record.Status,
			&record.Cost,
			&record.CostUnit,
			&record.DurationMillis,
			&record.Parameters,
			&record.ErrorMessage,
			&record.OccurredAt,
			&record.CreatedAt,
			&record.UpdatedAt,
		); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan call log record")
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate call log rows")
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

// runMigrations creates call log table and indexes when absent.
func runMigrations(ctx context.Context, db DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS mcp_call_logs (
			id UUID PRIMARY KEY,
			tool_name VARCHAR(64) NOT NULL,
			api_key_hash CHAR(64),
			key_prefix VARCHAR(16),
			status VARCHAR(16) NOT NULL,
			cost BIGINT NOT NULL,
			cost_unit VARCHAR(16) NOT NULL DEFAULT 'quota',
			duration_millis BIGINT,
			parameters JSONB,
			error_message TEXT,
			occurred_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_tool_name ON mcp_call_logs (tool_name)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_api_key_hash ON mcp_call_logs (api_key_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_key_prefix ON mcp_call_logs (key_prefix)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_status ON mcp_call_logs (status)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_occurred_at ON mcp_call_logs (occurred_at DESC)`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(ctx, stmt); err != nil {
			return errors.Wrap(err, "execute call log migration")
		}
	}

	return nil
}

var _ DB = (*pgxpool.Pool)(nil)

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
