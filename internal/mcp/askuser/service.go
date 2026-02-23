package askuser

import (
	"context"
	"database/sql"
	"time"

	errors "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/google/uuid"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

const (
	defaultPollInterval = time.Second
	// defaultListLimit caps the number of pending requests returned in a list.
	defaultListLimit = 50
	// defaultHistoryLimit caps the number of history records returned.
	defaultHistoryLimit = 100
)

// Service provides persistence and coordination helpers for ask_user requests.
type Service struct {
	db        *sql.DB
	logger    logSDK.Logger
	notifiers []Notifier
}

// Notifier receives lifecycle events for ask_user requests.
type Notifier interface {
	OnNewRequest(req *Request)
	OnRequestCancelled(req *Request)
}

// RegisterNotifier adds a listener for request lifecycle events.
func (s *Service) RegisterNotifier(n Notifier) {
	s.notifiers = append(s.notifiers, n)
}

// NewService constructs the service and runs the required migrations.
func NewService(db *sql.DB, logger logSDK.Logger) (*Service, error) {
	if db == nil {
		return nil, errors.New("sql db is required")
	}
	if logger == nil {
		logger = log.Logger.Named("ask_user_service")
	}

	if err := runMigrations(context.Background(), db); err != nil {
		return nil, errors.Wrap(err, "migrate ask_user tables")
	}

	return &Service{db: db, logger: logger}, nil
}

// CreateRequest persists a new question raised by an AI agent.
func (s *Service) CreateRequest(ctx context.Context, auth *AuthorizationContext, question string) (*Request, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}
	now := time.Now().UTC()
	req := &Request{
		ID:           gutils.UUID7Bytes(),
		Question:     question,
		Status:       StatusPending,
		APIKeyHash:   auth.APIKeyHash,
		KeySuffix:    auth.KeySuffix,
		UserIdentity: auth.UserIdentity,
		AIIdentity:   auth.AIIdentity,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	const insertSQL = `
INSERT INTO requests (
  id, question, answer, status, api_key_hash, key_suffix, user_identity, ai_identity, created_at, updated_at, answered_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
`
	if _, err := s.db.ExecContext(
		ctx,
		insertSQL,
		req.ID.String(),
		req.Question,
		nil,
		req.Status,
		req.APIKeyHash,
		req.KeySuffix,
		req.UserIdentity,
		req.AIIdentity,
		req.CreatedAt,
		req.UpdatedAt,
		nil,
	); err != nil {
		return nil, errors.Wrap(err, "create ask_user request")
	}
	s.log().Info("ask_user request created",
		zap.String("request_id", req.ID.String()),
		zap.String("user", auth.UserIdentity),
		zap.String("ai", auth.AIIdentity),
	)

	for _, n := range s.notifiers {
		n.OnNewRequest(req)
	}

	return req, nil
}

// WaitForAnswer blocks until the referenced request transitions to answered or the context is cancelled.
func (s *Service) WaitForAnswer(ctx context.Context, id uuid.UUID) (*Request, error) {
	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	for {
		req, err := s.getByID(ctx, id)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		switch req.Status {
		case StatusAnswered:
			return req, nil
		case StatusCancelled, StatusExpired:
			return nil, errors.Errorf("request %s closed with status %s", id, req.Status)
		}

		select {
		case <-ctx.Done():
			return nil, errors.WithStack(ctx.Err())
		case <-ticker.C:
		}
	}
}

// CancelRequest marks a request as cancelled when the caller stops waiting.
func (s *Service) CancelRequest(ctx context.Context, id uuid.UUID, status string) error {
	allowed := map[string]bool{StatusCancelled: true, StatusExpired: true}
	if !allowed[status] {
		status = StatusCancelled
	}

	// First, fetch the request to notify listeners
	req, err := s.getByID(ctx, id)
	if err != nil {
		return errors.Wrap(err, "fetch request for cancellation")
	}

	if req.Status != StatusPending {
		return nil
	}

	const updateSQL = `
UPDATE requests
SET status = $1, updated_at = $2
WHERE id = $3 AND status = $4
`
	if _, err := s.db.ExecContext(ctx, updateSQL, status, time.Now().UTC(), id.String(), StatusPending); err != nil {
		return errors.Wrap(err, "cancel ask_user request")
	}

	// Update local object for notification
	req.Status = status
	for _, n := range s.notifiers {
		n.OnRequestCancelled(req)
	}

	return nil
}

// ListRequests returns pending and recent records for the provided authorization scope.
func (s *Service) ListRequests(ctx context.Context, auth *AuthorizationContext) ([]Request, []Request, error) {
	if auth == nil {
		return nil, nil, ErrInvalidAuthorization
	}

	pending, err := listRequestsByStatus(ctx, s.db, auth.APIKeyHash, StatusPending, "created_at ASC", defaultListLimit, false)
	if err != nil {
		return nil, nil, errors.Wrap(err, "query pending requests")
	}

	history, err := listRequestsByStatus(ctx, s.db, auth.APIKeyHash, StatusPending, "updated_at DESC", defaultHistoryLimit, true)
	if err != nil {
		return nil, nil, errors.Wrap(err, "query history requests")
	}

	return pending, history, nil
}

// AnswerRequest stores the human response and marks the request as answered.
func (s *Service) AnswerRequest(ctx context.Context, auth *AuthorizationContext, id uuid.UUID, answer string) (*Request, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	req, err := getByIDAndAPIKeyHash(ctx, s.db, id, auth.APIKeyHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRequestNotFound
		}
		return nil, errors.Wrap(err, "lookup ask_user request")
	}

	if req.Status != StatusPending {
		return req, nil
	}

	now := time.Now().UTC()
	req.Answer = &answer
	req.Status = StatusAnswered
	req.AnsweredAt = &now

	const updateSQL = `
UPDATE requests
SET answer = $1, status = $2, answered_at = $3, updated_at = $4
WHERE id = $5 AND api_key_hash = $6
`
	if _, err := s.db.ExecContext(ctx, updateSQL, answer, StatusAnswered, now, now, id.String(), auth.APIKeyHash); err != nil {
		return nil, errors.Wrap(err, "update ask_user request with answer")
	}

	s.log().Info("ask_user request answered",
		zap.String("request_id", req.ID.String()),
		zap.String("user", auth.UserIdentity),
	)

	return req, nil
}

// getByID retrieves a request by ID within the provided context.
func (s *Service) getByID(ctx context.Context, id uuid.UUID) (*Request, error) {
	req, err := getByID(ctx, s.db, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRequestNotFound
		}
		return nil, errors.Wrap(err, "query ask_user request")
	}
	return req, nil
}

// GetRequest retrieves a request by ID.
func (s *Service) GetRequest(ctx context.Context, id uuid.UUID) (*Request, error) {
	return s.getByID(ctx, id)
}

func (s *Service) log() logSDK.Logger {
	return s.logger
}

// runMigrations creates ask_user tables and indexes without ORM dependencies.
func runMigrations(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS requests (
			id VARCHAR(36) PRIMARY KEY,
			question TEXT NOT NULL,
			answer TEXT NULL,
			status VARCHAR(16) NOT NULL,
			api_key_hash VARCHAR(64) NOT NULL,
			key_suffix VARCHAR(16) NOT NULL,
			user_identity VARCHAR(255) NOT NULL,
			ai_identity VARCHAR(255) NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			answered_at TIMESTAMP NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_requests_status ON requests (status)`,
		`CREATE INDEX IF NOT EXISTS idx_requests_api_key_hash ON requests (api_key_hash)`,
	}

	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return errors.Wrap(err, "run ask_user migrations")
		}
	}

	return nil
}

// listRequestsByStatus returns requests filtered by API key and status rule.
func listRequestsByStatus(ctx context.Context, db *sql.DB, apiKeyHash, status, orderExpr string, limit int, negate bool) ([]Request, error) {
	comparator := "="
	if negate {
		comparator = "<>"
	}

	query := `
SELECT id, question, answer, status, api_key_hash, key_suffix, user_identity, ai_identity, created_at, updated_at, answered_at
FROM requests
WHERE api_key_hash = $1 AND status ` + comparator + ` $2
ORDER BY ` + orderExpr + `
LIMIT $3
`

	rows, err := db.QueryContext(ctx, query, apiKeyHash, status, limit)
	if err != nil {
		return nil, errors.Wrap(err, "query requests")
	}
	defer rows.Close()

	requests := make([]Request, 0)
	for rows.Next() {
		req, scanErr := scanRequest(rows)
		if scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan request")
		}
		requests = append(requests, *req)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, errors.Wrap(rowsErr, "iterate request rows")
	}

	return requests, nil
}

// getByID retrieves one ask_user request by ID.
func getByID(ctx context.Context, db *sql.DB, id uuid.UUID) (*Request, error) {
	const query = `
SELECT id, question, answer, status, api_key_hash, key_suffix, user_identity, ai_identity, created_at, updated_at, answered_at
FROM requests
WHERE id = $1
LIMIT 1
`

	row := db.QueryRowContext(ctx, query, id.String())
	return scanRequestRow(row)
}

// getByIDAndAPIKeyHash retrieves one ask_user request by ID and API key hash.
func getByIDAndAPIKeyHash(ctx context.Context, db *sql.DB, id uuid.UUID, apiKeyHash string) (*Request, error) {
	const query = `
SELECT id, question, answer, status, api_key_hash, key_suffix, user_identity, ai_identity, created_at, updated_at, answered_at
FROM requests
WHERE id = $1 AND api_key_hash = $2
LIMIT 1
`

	row := db.QueryRowContext(ctx, query, id.String(), apiKeyHash)
	return scanRequestRow(row)
}

// scanRow abstracts row scanners shared by QueryRow and Rows.
type scanRow interface {
	Scan(dest ...any) error
}

// scanRequest reads one Request row from a scanner.
func scanRequest(row scanRow) (*Request, error) {
	return scanRequestRow(row)
}

// scanRequestRow parses common request fields from a DB row.
func scanRequestRow(row scanRow) (*Request, error) {
	var (
		idStr      string
		answer     sql.NullString
		answeredAt sql.NullTime
		req        Request
	)

	if err := row.Scan(
		&idStr,
		&req.Question,
		&answer,
		&req.Status,
		&req.APIKeyHash,
		&req.KeySuffix,
		&req.UserIdentity,
		&req.AIIdentity,
		&req.CreatedAt,
		&req.UpdatedAt,
		&answeredAt,
	); err != nil {
		return nil, errors.WithStack(err)
	}

	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, errors.Wrap(err, "parse request id")
	}
	req.ID = id

	if answer.Valid {
		req.Answer = &answer.String
	}
	if answeredAt.Valid {
		t := answeredAt.Time.UTC()
		req.AnsweredAt = &t
	}

	req.CreatedAt = req.CreatedAt.UTC()
	req.UpdatedAt = req.UpdatedAt.UTC()

	return &req, nil
}
