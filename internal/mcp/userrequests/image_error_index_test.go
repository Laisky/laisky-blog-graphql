package userrequests

import (
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// TestAttachmentErrorIndex ensures image failures identify the correct thumbnail.
func TestAttachmentErrorIndex(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    int
	}{
		{"attachment index 0: decode failed", 0},
		{"upload: attachment index 12: decode failed", 12},
		{"attachment index 3", 3},
		{"decode failed", -1},
		{"attachment index -1", -1},
		{"attachment index nope", -1},
		{"attachment index 9999999999999999999999999999999", -1},
		{"attachment index 1x", -1},
	} {
		t.Run(tc.message, func(t *testing.T) {
			err := errors.Wrap(errors.New(tc.message), "process request")
			require.Equal(t, tc.want, extractAttachmentIndex(err))
		})
	}
}
