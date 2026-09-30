package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

// TestMongoTweetIDFilterWireContract preserves literal string matching for
// ordinary, empty, leading-zero and operator-shaped IDs without coercion.
func TestMongoTweetIDFilterWireContract(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "01234567890123456789", `{"$ne":null}`, "$where", "a.b", "\x00"} {
		t.Run(id, func(t *testing.T) {
			wire, err := bson.Marshal(tweetIDFilter{ID: id})
			require.NoError(t, err)
			var result bson.M
			require.NoError(t, bson.Unmarshal(wire, &result))
			require.Equal(t, bson.M{"id_str": id}, result)
		})
	}
}
