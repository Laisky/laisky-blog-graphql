package web

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
)

func TestLegacyBlogLoginIsRemoved(t *testing.T) {
	schema := NewExecutableSchema(Config{}).Schema()
	_, err := gqlparser.LoadQuery(schema, "mutation { BlogLogin(account: \"legacy\", password: \"password\") { token } }")
	require.NotEmpty(t, err, "legacy mutation must be rejected before resolver execution")
	require.Contains(t, err.Error(), "Cannot query field")
	_, err = gqlparser.LoadQuery(schema, "mutation { UserLogin(account: \"user\", password: \"password\") { token } }")
	require.Empty(t, err, "modern login schema remains compatible")
}
