// Package firestore provides firestore client.
package firestore

import (
	"context"

	fsSDK "cloud.google.com/go/firestore"
	"github.com/Laisky/errors/v2"
	"google.golang.org/api/option"
)

type DB struct {
	*fsSDK.Client
	projectID string
}

// NewDB create firestore client
func NewDB(ctx context.Context, projectID string, opts ...option.ClientOption) (db *DB, err error) {
	db = &DB{
		projectID: projectID,
	}
	var cli *fsSDK.Client
	if cli, err = fsSDK.NewClient(ctx, projectID, opts...); err != nil {
		return nil, errors.Wrap(err, "create firestore client")
	}

	db.Client = cli
	return db, nil
}
