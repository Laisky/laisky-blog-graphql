package models

import (
	"context"

	"cloud.google.com/go/firestore"
	"github.com/Laisky/go-utils"
	"github.com/pkg/errors"
)

type Firestore struct {
	*firestore.Client
	projectID string
}

func NewFirestore(ctx context.Context, projectID string) (db *Firestore, err error) {
	db = &Firestore{
		projectID: projectID,
	}
	var cli *firestore.Client
	if cli, err = firestore.NewClient(
		ctx,
		utils.Settings.GetString("settings.gcp.project_id"),
	); err != nil {
		return nil, errors.Wrap(err, "create firestore client")
	}

	db.Client = cli
	return db, nil
}
