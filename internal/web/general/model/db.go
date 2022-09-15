package model

import (
	"context"
	"path/filepath"

	"github.com/Laisky/laisky-blog-graphql/library/db"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config"
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
		gconfig.Shared.GetString("settings.general.project_id"),
		option.WithCredentialsFile(filepath.Join(
			gconfig.Shared.GetString("cfg_dir"),
			gconfig.Shared.GetString("settings.general.credential_file"),
		)),
	); err != nil {
		log.Logger.Panic("create firestore client", zap.Error(err))
	}
}
