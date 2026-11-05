package dao

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

// storedIndexOf decodes doc as a listIndexes entry, as EnsureIndexes reads one.
func storedIndexOf(t *testing.T, doc bson.D) storedIndex {
	t.Helper()
	raw, err := bson.Marshal(doc)
	require.NoError(t, err)
	var ix storedIndex
	require.NoError(t, bson.Unmarshal(raw, &ix))
	return ix
}

// TestMonitorIndexesCoverAlertLookups pins the indexes the alert entrypoints'
// lookups rely on. alert_types.name must stay unique, because CreateAlertType
// upserts by name and only a unique index keeps concurrent creations of one
// name to one document.
func TestMonitorIndexesCoverAlertLookups(t *testing.T) {
	require.Equal(t, []monitorIndex{
		{collection: "alert_types", field: "name", unique: true},
		{collection: "user_alert_relations", field: "alert_id"},
	}, monitorIndexes)
	require.Equal(t, "name_1", monitorIndexes[0].name(), "the name createIndex({name: 1}) gives by hand")
}

// TestMonitorIndexMatching covers how EnsureIndexes judges an index already
// present: whatever its name, a single-field index on the field serves the
// lookup and is kept, and only the ways it falls short are reported. Option
// values follow MongoDB's reading, where a number counts as true unless zero.
func TestMonitorIndexMatching(t *testing.T) {
	unique := monitorIndex{collection: "alert_types", field: "name", unique: true}
	plain := monitorIndex{collection: "user_alert_relations", field: "alert_id"}
	cases := []struct {
		name     string
		want     monitorIndex
		doc      bson.D
		locale   string
		covers   bool
		problems []string
	}{
		{"created by EnsureIndexes", unique, bson.D{{Key: "name", Value: "name_1"}, {Key: "key", Value: bson.D{{Key: "name", Value: int32(1)}}}, {Key: "unique", Value: true}}, "simple", true, nil},
		{"operator's name and direction", unique, bson.D{{Key: "name", Value: "by_name"}, {Key: "key", Value: bson.D{{Key: "name", Value: -1.0}}}, {Key: "unique", Value: int32(1)}}, "simple", true, nil},
		{"not unique", unique, bson.D{{Key: "name", Value: "name_1"}, {Key: "key", Value: bson.D{{Key: "name", Value: int64(1)}}}}, "simple", true, []string{"not unique"}},
		{"unique option zero", unique, bson.D{{Key: "name", Value: "name_1"}, {Key: "key", Value: bson.D{{Key: "name", Value: 1}}}, {Key: "unique", Value: int32(0)}}, "simple", true, []string{"not unique"}},
		{"plain index needs no uniqueness", plain, bson.D{{Key: "name", Value: "alert_id_1"}, {Key: "key", Value: bson.D{{Key: "alert_id", Value: 1}}}}, "simple", true, nil},
		{"hidden and partial", unique, bson.D{{Key: "name", Value: "name_1"}, {Key: "key", Value: bson.D{{Key: "name", Value: 1}}}, {Key: "unique", Value: true},
			{Key: "hidden", Value: true}, {Key: "partialFilterExpression", Value: bson.D{{Key: "name", Value: bson.D{{Key: "$exists", Value: true}}}}}}, "simple", true,
			[]string{"hidden from the query planner", "partial"}},
		{"other collation", unique, bson.D{{Key: "name", Value: "name_1"}, {Key: "key", Value: bson.D{{Key: "name", Value: 1}}}, {Key: "unique", Value: true},
			{Key: "collation", Value: bson.D{{Key: "locale", Value: "fr"}}}}, "simple", true, []string{"collation fr differs from the collection's simple"}},
		{"collection's own collation", unique, bson.D{{Key: "name", Value: "name_1"}, {Key: "key", Value: bson.D{{Key: "name", Value: 1}}}, {Key: "unique", Value: true},
			{Key: "collation", Value: bson.D{{Key: "locale", Value: "fr"}}}}, "fr", true, nil},
		{"compound", unique, bson.D{{Key: "name", Value: "name_1_x_1"}, {Key: "key", Value: bson.D{{Key: "name", Value: 1}, {Key: "x", Value: 1}}}, {Key: "unique", Value: true}}, "simple", false, nil},
		{"hashed", plain, bson.D{{Key: "name", Value: "alert_id_hashed"}, {Key: "key", Value: bson.D{{Key: "alert_id", Value: "hashed"}}}}, "simple", false, nil},
		{"other field", unique, bson.D{{Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: 1}}}}, "simple", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ix := storedIndexOf(t, tc.doc)
			require.Equal(t, tc.covers, ix.covers(tc.want.field))
			if tc.covers {
				require.Equal(t, tc.problems, ix.problems(tc.want, tc.locale))
			}
		})
	}
}

// TestFindIndexPrefersTheOneThatServesLookups keeps an operator's usable index
// from being reported because a hidden one on the same field is listed first,
// and reports nothing found when no index covers the field.
func TestFindIndexPrefersTheOneThatServesLookups(t *testing.T) {
	want := monitorIndex{collection: "alert_types", field: "name", unique: true}
	existing := []storedIndex{
		storedIndexOf(t, bson.D{{Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: 1}}}}),
		storedIndexOf(t, bson.D{{Key: "name", Value: "name_1"}, {Key: "key", Value: bson.D{{Key: "name", Value: 1}}}, {Key: "hidden", Value: true}}),
		storedIndexOf(t, bson.D{{Key: "name", Value: "name_-1"}, {Key: "key", Value: bson.D{{Key: "name", Value: -1}}}, {Key: "unique", Value: true}}),
	}
	found, problems, ok := findIndex(existing, want, "simple")
	require.True(t, ok)
	require.Equal(t, "name_-1", found.Name)
	require.Empty(t, problems)

	_, _, ok = findIndex(existing[:1], want, "simple")
	require.False(t, ok)
}
