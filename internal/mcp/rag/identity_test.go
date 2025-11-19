package rag

import "testing"

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
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id.UserID == "" {
				t.Fatalf("user id should not be empty")
			}
			if tc.expectTask != "" && id.TaskID != tc.expectTask {
				t.Fatalf("unexpected task id: %s", id.TaskID)
			}
			if id.APIKey == "" || id.MaskedID == "" {
				t.Fatalf("missing key fields")
			}
		})
	}
}
