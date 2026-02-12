package files

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateProject verifies project validation rules.
func TestValidateProject(t *testing.T) {
	require.NoError(t, ValidateProject("proj_1"))
	require.Error(t, ValidateProject(""))
	require.Error(t, ValidateProject("bad space"))
	require.Error(t, ValidateProject("项目"))
}

// TestValidatePath verifies path validation rules.
func TestValidatePath(t *testing.T) {
	valid := []string{"", "/a", "/a/b.txt", "/a-b_c.d"}
	for _, path := range valid {
		require.NoError(t, ValidatePath(path))
	}
	invalid := []string{"a", "/a/", "/a//b", "/a/../b", "/a/./b", "/a/b c", "/a/\x7fb", "/你好"}
	for _, path := range invalid {
		require.Error(t, ValidatePath(path))
	}
}
