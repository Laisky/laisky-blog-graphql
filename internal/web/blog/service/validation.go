package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dto"
)

const (
	// maxPostPageSize caps the number of posts returned in one page.
	maxPostPageSize = 200
	// maxPostNameLength caps the length of post names.
	maxPostNameLength = 200
	// maxPostTitleLength caps the length of post titles.
	maxPostTitleLength = 200
	// maxPostTagLength caps the length of post tags.
	maxPostTagLength = 100
	// maxPostRegexpLength caps the length of post regex filters.
	maxPostRegexpLength = 256
	// maxCategoryNameLength caps the length of category names.
	maxCategoryNameLength = 200
	// maxCategoryURLLength caps the length of category URLs.
	maxCategoryURLLength = 200
	// maxPostContentLengthLimit caps the length of post content used for filters.
	maxPostContentLengthLimit = 10000
	// maxPostSeriesKeyLength caps the length of post series keys.
	maxPostSeriesKeyLength = 100
	// maxCommentPageSize caps the number of comments returned in one page.
	maxCommentPageSize = 200
	// maxCommentContentLength caps the length of comment content.
	maxCommentContentLength = 10000
	// maxCommentAuthorNameLen caps the length of comment author names.
	maxCommentAuthorNameLen = 100
	// maxCommentAuthorEmailLen caps the length of comment author emails.
	maxCommentAuthorEmailLen = 254
	// maxCommentWebsiteLen caps the length of comment author websites.
	maxCommentWebsiteLen = 2048
	// maxUserAccountLength caps the length of user account identifiers.
	maxUserAccountLength = 128
	// maxUserPasswordLength caps the length of user passwords.
	maxUserPasswordLength = 1024
	// maxUserDisplayNameLength caps the length of user display names.
	maxUserDisplayNameLength = 128
	// maxActiveTokenLength caps the length of account activation tokens.
	maxActiveTokenLength = 256
)

var (
	commentSortFields = map[string]string{
		"created_at": "created_at",
		"likes":      "likes",
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

// sanitizeRequiredText trims input, enforces maxLen runes, and returns the sanitized value or an error.
// It accepts the raw input string, a rune length limit, and a field label, returning the sanitized string.
func sanitizeRequiredText(input string, maxLen int, field string) (string, error) {
	trimmed, err := sanitizeOptionalText(input, maxLen, field)
	if err != nil {
		return "", err
	}
	if trimmed == "" {
		return "", errors.Errorf("%s is required", field)
	}
	return trimmed, nil
}

// sanitizePagination validates page and size bounds and returns the sanitized values or an error.
// It accepts the requested page, size, and maximum size, returning the validated page and size.
func sanitizePagination(page int, size int, maxSize int) (int, int, error) {
	if page < 0 {
		return 0, 0, errors.New("page must be non-negative")
	}
	if size < 0 || size > maxSize {
		return 0, 0, errors.Errorf("size must be within [0~%d]", maxSize)
	}
	return page, size, nil
}

// sanitizeLength validates that length is non-negative and caps it at maxLen when needed.
// It accepts the input length and maxLen, returning the sanitized length or an error.
func sanitizeLength(length int, maxLen int) (int, error) {
	if length < 0 {
		return 0, errors.New("length must be non-negative")
	}
	if length > maxLen {
		return maxLen, nil
	}
	return length, nil
}

// sanitizeRegex trims and validates a regex pattern, returning the sanitized pattern or an error.
// It accepts the raw pattern and a rune length limit, returning the cleaned pattern string.
func sanitizeRegex(pattern string, maxLen int) (string, error) {
	trimmed, err := sanitizeOptionalText(pattern, maxLen, "regexp")
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

// normalizePostNameForQuery trims and validates a post name, then returns its query-escaped lowercased form.
// It accepts the raw post name and returns the normalized name for database queries.
func normalizePostNameForQuery(name string) (string, error) {
	trimmed, err := sanitizeRequiredText(name, maxPostNameLength, "post name")
	if err != nil {
		return "", err
	}
	return strings.ToLower(url.QueryEscape(trimmed)), nil
}

// sanitizePostCfg validates user-provided post configuration and returns a sanitized copy.
// It accepts a post config pointer and returns a sanitized config or an error.
func sanitizePostCfg(cfg *dto.PostCfg) (*dto.PostCfg, error) {
	if cfg == nil {
		return nil, errors.New("post config is nil")
	}
	normalized := *cfg
	page, size, err := sanitizePagination(cfg.Page, cfg.Size, maxPostPageSize)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize pagination")
	}
	length, err := sanitizeLength(cfg.Length, maxPostContentLengthLimit)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize length")
	}
	name, err := sanitizeOptionalText(cfg.Name, maxPostNameLength, "post name")
	if err != nil {
		return nil, errors.Wrap(err, "sanitize post name")
	}
	tag, err := sanitizeOptionalText(cfg.Tag, maxPostTagLength, "tag")
	if err != nil {
		return nil, errors.Wrap(err, "sanitize tag")
	}
	regexpValue, err := sanitizeRegex(cfg.Regexp, maxPostRegexpLength)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize regexp")
	}
	var categoryURL *string
	if cfg.CategoryURL != nil {
		trimmed, err := sanitizeOptionalText(*cfg.CategoryURL, maxCategoryURLLength, "category url")
		if err != nil {
			return nil, errors.Wrap(err, "sanitize category url")
		}
		categoryURL = &trimmed
	}

	normalized.Page = page
	normalized.Size = size
	normalized.Length = length
	normalized.Name = name
	normalized.Tag = tag
	normalized.Regexp = regexpValue
	normalized.CategoryURL = categoryURL
	return &normalized, nil
}

// sanitizePostSeriesKey trims and validates a post series key for database queries.
// It accepts the raw key and returns the sanitized key or an error.
func sanitizePostSeriesKey(key string) (string, error) {
	return sanitizeOptionalText(key, maxPostSeriesKeyLength, "post series key")
}

// sanitizePostTitle validates a post title and returns the sanitized value or an error.
// It accepts the raw title string and returns the sanitized title.
func sanitizePostTitle(title string) (string, error) {
	return sanitizeRequiredText(title, maxPostTitleLength, "post title")
}

// sanitizePostType validates a post type and returns a normalized type string.
// It accepts the raw type string and returns the normalized type or an error.
func sanitizePostType(ptype string) (string, error) {
	trimmed, err := sanitizeOptionalText(ptype, maxPostTagLength, "post type")
	if err != nil {
		return "", err
	}
	if trimmed == "" {
		return "html", nil
	}
	normalized := strings.ToLower(trimmed)
	switch normalized {
	case "markdown", "slide", "html":
		return normalized, nil
	default:
		return "", errors.Errorf("invalid post type %q", trimmed)
	}
}

// sanitizeCommentSortField validates the sort field against allowlisted values and returns a safe field name.
// It accepts the raw sort field and returns the normalized field name for queries.
func sanitizeCommentSortField(field string) string {
	trimmed := strings.ToLower(strings.TrimSpace(field))
	if trimmed == "" {
		return "created_at"
	}
	if mapped, ok := commentSortFields[trimmed]; ok {
		return mapped
	}
	return "created_at"
}

// sanitizeEmail trims and validates an email address string, returning the sanitized email or an error.
// It accepts the raw email string and returns the validated email string.
func sanitizeEmail(email string) (string, error) {
	trimmed, err := sanitizeRequiredText(email, maxCommentAuthorEmailLen, "author email")
	if err != nil {
		return "", errors.WithStack(err)
	}
	parsed, err := mail.ParseAddress(trimmed)
	if err != nil {
		return "", errors.Wrap(err, "invalid email")
	}
	return parsed.Address, nil
}

// sanitizeWebsite trims and validates an optional website URL, returning the sanitized URL or nil.
// It accepts the raw website string pointer and returns the sanitized pointer or an error.
func sanitizeWebsite(website *string) (*string, error) {
	if website == nil {
		return nil, nil
	}
	trimmed, err := sanitizeOptionalText(*website, maxCommentWebsiteLen, "author website")
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if trimmed == "" {
		return nil, nil
	}
	if _, err := url.ParseRequestURI(trimmed); err != nil {
		return nil, errors.Wrap(err, "invalid website url")
	}
	return &trimmed, nil
}

// sanitizeCommentBody validates the comment content and returns the sanitized content or an error.
// It accepts the raw comment content string and returns the sanitized content.
func sanitizeCommentBody(body string) (string, error) {
	return sanitizeRequiredText(body, maxCommentContentLength, "comment content")
}

// sanitizeAuthorName validates a comment author name and returns the sanitized value or an error.
// It accepts the raw author name and returns the sanitized name.
func sanitizeAuthorName(name string) (string, error) {
	return sanitizeRequiredText(name, maxCommentAuthorNameLen, "author name")
}

// sanitizeUserAccount validates a user account identifier and returns the sanitized value or an error.
// It accepts the raw account string and returns the sanitized account.
func sanitizeUserAccount(account string) (string, error) {
	trimmed, err := sanitizeRequiredText(account, maxUserAccountLength, "account")
	if err != nil {
		return "", err
	}
	return strings.ToLower(trimmed), nil
}

// sanitizeUserPassword validates a user password string and returns the sanitized value or an error.
// It accepts the raw password string and returns the sanitized password.
func sanitizeUserPassword(password string) (string, error) {
	return sanitizeRequiredText(password, maxUserPasswordLength, "password")
}

// sanitizeUserDisplayName validates an optional display name and returns the sanitized value or an error.
// It accepts the raw display name string and returns the sanitized display name.
func sanitizeUserDisplayName(displayName string) (string, error) {
	return sanitizeOptionalText(displayName, maxUserDisplayNameLength, "display name")
}

// sanitizeActiveToken validates an activation token string and returns the sanitized value or an error.
// It accepts the raw token string and returns the sanitized token.
func sanitizeActiveToken(token string) (string, error) {
	return sanitizeRequiredText(token, maxActiveTokenLength, "active token")
}

// secureCompareString performs a constant-time comparison of two strings and returns true when they match.
// It accepts two strings and returns a boolean indicating equality.
func secureCompareString(left string, right string) bool {
	leftSum := sha256.Sum256([]byte(left))
	rightSum := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftSum[:], rightSum[:]) == 1
}
