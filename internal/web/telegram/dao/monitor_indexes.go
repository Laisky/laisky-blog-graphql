package dao

import (
	"context"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// monitorIndex is a single-field index that the monitor's lookups rely on.
type monitorIndex struct {
	// collection holds the indexed field.
	collection string
	// field is the indexed field.
	field string
	// unique also has MongoDB keep one document per value.
	unique bool
}

// name returns the index name, the one MongoDB gives an ascending single-field
// index by default, so an index made by hand with createIndex({field: 1}) has
// the same name.
func (ix monitorIndex) name() string { return ix.field + "_1" }

// monitorIndexes lists the indexes behind the alert entrypoints' lookups.
//
//   - alert_types.name serves every credential check by name: the GraphQL
//     mutation's, the webhook's, and the bounded fallback lookup's. It is unique
//     because CreateAlertType upserts by name, and MongoDB only keeps concurrent
//     upserts of one value to one document when a unique index covers the filter.
//   - user_alert_relations.alert_id serves the subscriber lookup of every
//     delivered alert.
var monitorIndexes = []monitorIndex{
	{collection: alertTypeColName, field: fieldName, unique: true},
	{collection: userAlertRelationColName, field: fieldAlertId},
}

// storedIndex is the part of a listIndexes entry that EnsureIndexes reads.
type storedIndex struct {
	Name      string        `bson:"name"`
	Key       bson.D        `bson:"key"`
	Unique    bson.RawValue `bson:"unique"`
	Hidden    bson.RawValue `bson:"hidden"`
	Partial   bson.Raw      `bson:"partialFilterExpression"`
	Collation bson.Raw      `bson:"collation"`
}

// covers reports whether the index's key is field alone, in either direction.
// Such an index serves an equality lookup on field, whatever its name.
func (ix storedIndex) covers(field string) bool {
	if len(ix.Key) != 1 || ix.Key[0].Key != field {
		return false
	}
	switch direction := ix.Key[0].Value.(type) {
	case int32:
		return direction == 1 || direction == -1
	case int64:
		return direction == 1 || direction == -1
	case float64:
		return direction == 1 || direction == -1
	default:
		// "hashed", "text" and the geospatial types are other key patterns.
		return false
	}
}

// problems lists how the index falls short of want on a collection whose
// default collation has collectionLocale. An empty list means it serves want's
// lookups as an index EnsureIndexes creates would.
func (ix storedIndex) problems(want monitorIndex, collectionLocale string) []string {
	var problems []string
	if want.unique && !truthy(ix.Unique) {
		problems = append(problems, "not unique")
	}
	if truthy(ix.Hidden) {
		problems = append(problems, "hidden from the query planner")
	}
	if len(ix.Partial) > 0 {
		problems = append(problems, "partial")
	}
	// A lookup uses the collection's default collation, and an index with
	// another collation cannot serve its string comparisons.
	if locale := collationLocale(ix.Collation); locale != collectionLocale {
		problems = append(problems, "collation "+locale+" differs from the collection's "+collectionLocale)
	}
	return problems
}

// collationLocale returns a collation document's locale, "simple" when there is
// no collation, which is MongoDB's binary comparison.
func collationLocale(collation bson.Raw) string {
	if locale, ok := collation.Lookup("locale").StringValueOK(); ok {
		return locale
	}
	return "simple"
}

// truthy reads an index option as MongoDB does: a missing option is false, and
// a number counts as true unless it is zero.
func truthy(value bson.RawValue) bool {
	if b, ok := value.BooleanOK(); ok {
		return b
	}
	if n, ok := value.Int32OK(); ok {
		return n != 0
	}
	if n, ok := value.Int64OK(); ok {
		return n != 0
	}
	if n, ok := value.DoubleOK(); ok {
		return n != 0
	}
	return false
}

// findIndex returns the index in existing that covers want's field with the
// fewest problems, and whether there is one.
func findIndex(existing []storedIndex, want monitorIndex, collectionLocale string) (best storedIndex, problems []string, found bool) {
	for _, ix := range existing {
		if !ix.covers(want.field) {
			continue
		}
		if p := ix.problems(want, collectionLocale); !found || len(p) < len(problems) {
			best, problems, found = ix, p, true
		}
	}
	return best, problems, found
}

// EnsureIndexes makes sure the monitor collections carry the indexes the alert
// entrypoints' lookups rely on, so a lookup by name reads one index entry
// instead of scanning the collection. It is idempotent and safe to run on every
// start:
//   - An index already present on the field is kept as it is, whatever its name
//     or origin. If it falls short (not unique, hidden, partial, or another
//     collation than the collection's), that is logged for an operator to
//     resolve, and nothing is changed.
//   - A missing index is created.
//   - When stored documents prevent a unique index, because they repeat or lack
//     the field, the index is created without uniqueness, so lookups are still
//     indexed and no document is touched. That is logged as an error until an
//     operator removes the duplicates and the index, after which the next start
//     creates it unique.
//
// Any other storage failure is returned. The index builds run within ctx, which
// the caller should bound.
func (d *Monitor) EnsureIndexes(ctx context.Context) error {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_ensure_indexes")
	for _, want := range monitorIndexes {
		if err := d.ensureIndex(ctx, logger, want); err != nil {
			return errors.Wrapf(err, "ensure index %s on %s", want.name(), want.collection)
		}
	}
	return nil
}

// ensureIndex makes sure want exists, as EnsureIndexes describes.
func (d *Monitor) ensureIndex(ctx context.Context, logger glog.Logger, want monitorIndex) error {
	col := d.db.GetCol(want.collection)
	indexes := col.Indexes()
	cur, err := indexes.List(ctx)
	if err != nil {
		return errors.Wrap(err, "list indexes")
	}
	var existing []storedIndex
	if err = cur.All(ctx, &existing); err != nil {
		return errors.Wrap(err, "read indexes")
	}
	collectionLocale, err := defaultLocale(ctx, col)
	if err != nil {
		return errors.Wrap(err, "read collection options")
	}
	if found, problems, ok := findIndex(existing, want, collectionLocale); ok {
		if len(problems) > 0 {
			logger.Warn("monitor index falls short and is kept as it is",
				zap.String("collection", want.collection),
				zap.String("index", found.Name),
				zap.Strings("problems", problems))
			return nil
		}
		logger.Debug("monitor index present",
			zap.String("collection", want.collection), zap.String("index", found.Name))
		return nil
	}

	model := mongoLib.IndexModel{
		Keys:    bson.D{{Key: want.field, Value: 1}},
		Options: options.Index().SetName(want.name()).SetUnique(want.unique),
	}
	_, err = indexes.CreateOne(ctx, model)
	switch {
	case err == nil:
		logger.Info("create monitor index",
			zap.String("collection", want.collection),
			zap.String("index", want.name()),
			zap.Bool("unique", want.unique))
		return nil
	case !want.unique || !mongoLib.IsDuplicateKeyError(err):
		return errors.Wrap(err, "create index")
	}

	uniqueErr := err
	model.Options = options.Index().SetName(want.name())
	if _, err = indexes.CreateOne(ctx, model); err != nil {
		return errors.Wrap(err, "create index without uniqueness")
	}
	logger.Error("stored documents repeat or lack the indexed field, so the index was created without uniqueness; "+
		"remove the duplicates, then drop the index so the next start creates it unique",
		zap.String("collection", want.collection),
		zap.String("index", want.name()),
		zap.Error(uniqueErr))
	return nil
}

// defaultLocale returns the locale of col's default collation, which lookups
// without a collation of their own use. A collection that does not exist yet
// has none, so it compares as "simple".
func defaultLocale(ctx context.Context, col *mongoLib.Collection) (string, error) {
	collections, err := col.Database().ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: col.Name()}})
	if err != nil {
		return "", errors.WithStack(err)
	}
	if len(collections) != 1 {
		return collationLocale(nil), nil
	}
	collation, _ := collections[0].Options.Lookup("collation").DocumentOK()
	return collationLocale(collation), nil
}
