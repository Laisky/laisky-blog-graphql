package model

import (
	"context"
	"path/filepath"

	"laisky-blog-graphql/library/db"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"google.golang.org/api/option"
)

var (
	GeneralDB *db.Firestore
)

func Initialize(ctx context.Context) {
	defer log.Logger.Info("connected gcp firestore")
	var err error
	if GeneralDB, err = db.NewFirestore(
		ctx,
		gutils.Settings.GetString("settings.general.project_id"),
		option.WithCredentialsFile(filepath.Join(
			gutils.Settings.GetString("cfg_dir"),
			gutils.Settings.GetString("settings.general.credential_file"),
		)),
	); err != nil {
		log.Logger.Panic("create firestore client", zap.Error(err))
	}
}
