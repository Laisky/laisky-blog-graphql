package userrequests

import (
	"fmt"
	"time"

	errors "github.com/Laisky/errors/v2"
)

var supportedSQLTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

// parseSQLTime converts SQL driver values into UTC time values.
func parseSQLTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case string:
		return parseSQLTimeString(typed)
	case []byte:
		return parseSQLTimeString(string(typed))
	default:
		return time.Time{}, errors.Errorf("unsupported sql time type: %T", value)
	}
}

// parseNullableSQLTime converts SQL driver values into optional UTC time values.
func parseNullableSQLTime(value any) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}

	parsed, err := parseSQLTime(value)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &parsed, nil
}

// parseSQLTimeString parses supported SQL timestamp string encodings.
func parseSQLTimeString(raw string) (time.Time, error) {
	for _, layout := range supportedSQLTimeLayouts {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			return parsed.UTC(), nil
		}
	}

	return time.Time{}, errors.Errorf("unsupported sql time format: %s", fmt.Sprintf("%q", raw))
}
