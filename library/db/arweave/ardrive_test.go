package arweave

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_JsonRegexp(t *testing.T) {
	input := `(node:1474500) Warning: Setting the NODE_TLS_REJECT_UNAUTHORIZED environment variable to '0' makes TLS connections and HTTPS requests insecure by disabling certificate verification.
	(Use "node --trace-warnings ... to show where the warning was created)
	{
		"created": [
			{
				"123": "312"
			}
		]
	}`

	matched := jsonReg.FindAllSubmatch([]byte(input), -1)
	require.Len(t, matched, 1)
	require.Len(t, matched[0], 2)

	m := make(map[string]interface{})
	err := json.Unmarshal(matched[0][1], &m)
	require.NoError(t, err)
}
