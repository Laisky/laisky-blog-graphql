package service

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/dto"
)

const (
	// maxTweetPageSize caps the number of tweets returned per page.
	maxTweetPageSize = 100
	// maxTweetIDLength caps the length of tweet IDs.
	maxTweetIDLength = 32
	// maxTopicLength caps the length of topic filters.
	maxTopicLength = 100
	// maxUsernameLength caps the length of user name filters.
	maxUsernameLength = 100
	// maxRegexpLength caps the length of regex filters.
	maxRegexpLength = 256
	// maxViewerIDLength caps the length of viewer ID filters.
	maxViewerIDLength = 20
	// maxTweetIDMinChars enforces the minimum length of tweet IDs.
	maxTweetIDMinChars = 1
)

var (
	tweetSortFields = map[string]string{
		"created_at": "created_at",
	}
)

// sanitizeOptionalText trims input, checks for null bytes, enforces maxLen runes, and returns the sanitized value.
// It accepts the raw input string, a rune length limit, and a field label for error context, returning the sanitized string.
func sanitizeOptionalText(input string, maxLen int, field string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", nil
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", errors.Errorf("%s contains invalid null byte", field)
	}
	if utf8.RuneCountInString(trimmed) > maxLen {
		return "", errors.Errorf("%s exceeds max length %d", field, maxLen)
	}
	return trimmed, nil
}

// sanitizeTweetID validates a tweet ID string and returns the sanitized ID or an error.
// It accepts the raw tweet ID string and returns the sanitized ID string.
func sanitizeTweetID(id string) (string, error) {
	trimmed, err := sanitizeOptionalText(id, maxTweetIDLength, "tweet id")
	if err != nil {
		return "", err
	}
	if trimmed == "" {
		return "", errors.New("tweet id is required")
	}
	if utf8.RuneCountInString(trimmed) < maxTweetIDMinChars {
		return "", errors.New("tweet id is too short")
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return "", errors.New("tweet id must be numeric")
		}
	}
	return trimmed, nil
}

// sanitizeTopic validates a tweet topic string and returns the sanitized value or an error.
// It accepts the raw topic string and returns the sanitized topic.
func sanitizeTopic(topic string) (string, error) {
	return sanitizeOptionalText(topic, maxTopicLength, "topic")
}

// sanitizeUsername validates a username string and returns the sanitized value or an error.
// It accepts the raw username string and returns the sanitized username.
func sanitizeUsername(username string) (string, error) {
	return sanitizeOptionalText(username, maxUsernameLength, "username")
}

// sanitizeViewerID validates a viewer id string and returns the sanitized value or an error.
// It accepts the raw viewer id string and returns the sanitized viewer id.
func sanitizeViewerID(viewerID string) (string, error) {
	trimmed, err := sanitizeOptionalText(viewerID, maxViewerIDLength, "viewer id")
	if err != nil {
		return "", err
	}
	if trimmed == "" {
		return "", nil
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return "", errors.New("viewer id must be numeric")
		}
	}
	return trimmed, nil
}

// sanitizeRegexp validates an optional regex pattern and returns the sanitized pattern or an error.
// It accepts the raw pattern string and returns the sanitized pattern string.
func sanitizeRegexp(pattern string) (string, error) {
	trimmed, err := sanitizeOptionalText(pattern, maxRegexpLength, "regexp")
	if err != nil {
		return "", err
	}
	if trimmed == "" {
		return "", nil
	}
	if _, err = regexp.Compile(trimmed); err != nil {
		return "", errors.Wrap(err, "invalid regexp")
	}
	return trimmed, nil
}

// sanitizePagination validates page and size bounds and returns the sanitized values or an error.
// It accepts the requested page and size and returns the validated page and size.
func sanitizePagination(page int, size int) (int, int, error) {
	if page < 0 {
		return 0, 0, errors.New("page must be non-negative")
	}
	if size < 0 || size > maxTweetPageSize {
		return 0, 0, errors.Errorf("size must be within [0~%d]", maxTweetPageSize)
	}
	return page, size, nil
}

// sanitizeTweetSortField validates the sort field against allowlisted values and returns a safe field name.
// It accepts the raw sort field and returns the normalized field name.
func sanitizeTweetSortField(field string) string {
	trimmed := strings.ToLower(strings.TrimSpace(field))
	if trimmed == "" {
		return "created_at"
	}
	if mapped, ok := tweetSortFields[trimmed]; ok {
		return mapped
	}
	return "created_at"
}

// sanitizeTweetSortOrder validates the sort order string and returns a normalized order or an error.
// It accepts the raw sort order and returns the normalized order string.
func sanitizeTweetSortOrder(order string) (string, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(order))
	if trimmed == "" {
		return "DESC", nil
	}
	switch trimmed {
	case "ASC", "DESC":
		return trimmed, nil
	default:
		return "", errors.Errorf("SortOrder must be ASC or DESC, but got %s", order)
	}
}

// sanitizeLoadTweetArgs validates user-provided tweet query args and returns a sanitized copy.
// It accepts the raw args pointer and returns a sanitized copy or an error.
func sanitizeLoadTweetArgs(cfg *dto.LoadTweetArgs) (*dto.LoadTweetArgs, error) {
	if cfg == nil {
		return nil, errors.New("tweet args is nil")
	}
	normalized := *cfg
	page, size, err := sanitizePagination(cfg.Page, cfg.Size)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize pagination")
	}
	topic, err := sanitizeTopic(cfg.Topic)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize topic")
	}
	username, err := sanitizeUsername(cfg.Username)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize username")
	}
	viewerID, err := sanitizeViewerID(cfg.ViewerID)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize viewer id")
	}
	regexpValue, err := sanitizeRegexp(cfg.Regexp)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize regexp")
	}
	var tweetID string
	if cfg.TweetID != "" {
		if tweetID, err = sanitizeTweetID(cfg.TweetID); err != nil {
			return nil, errors.Wrap(err, "sanitize tweet id")
		}
	}
	sortOrder, err := sanitizeTweetSortOrder(cfg.SortOrder)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	normalized.Page = page
	normalized.Size = size
	normalized.Topic = topic
	normalized.Username = username
	normalized.ViewerID = viewerID
	normalized.Regexp = regexpValue
	normalized.TweetID = tweetID
	normalized.SortBy = sanitizeTweetSortField(cfg.SortBy)
	normalized.SortOrder = sortOrder
	return &normalized, nil
}
