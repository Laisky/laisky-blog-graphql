package rag

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseIdentity(t *testing.T) {
	cases := []struct {
		name       string
		header     string
		expectTask string
		expectErr  bool
	}{
		{
			name:   "basic token",
			header: "Bearer sk-example",
		},
		{
			name:       "with task prefix",
			header:     "Bearer workspace@sk-test",
			expectTask: "workspace",
		},
		{
			name:      "missing token",
			header:    "Bearer   ",
			expectErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := ParseIdentity(tc.header)
			if tc.expectErr {
				require.Error(t, err, "expected error")
				return
			}
			require.NoError(t, err, "unexpected error")
			require.NotEmpty(t, id.UserID, "user id should not be empty")
			if tc.expectTask != "" {
				require.Equal(t, tc.expectTask, id.TaskID, "unexpected task id")
			}
			require.NotEmpty(t, id.APIKey, "APIKey should not be empty")
			require.NotEmpty(t, id.MaskedID, "MaskedID should not be empty")
		})
	}
}
