package model

import (
	"context"

	fsDB "github.com/Laisky/laisky-blog-graphql/library/db/firestore"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

var (
	GeneralDB *fsDB.DB
)

func Initialize(ctx context.Context) {
	defer log.Logger.Info("connected gcp firestore")
	// if GeneralDB, err = fsDB.NewDB(
	// 	ctx,
	// 	gconfig.Shared.GetString("settings.general.project_id"),
	// 	option.WithCredentialsFile(filepath.Join(
	// 		gconfig.Shared.GetString("cfg_dir"),
	// 		gconfig.Shared.GetString("settings.general.credential_file"),
	// 	)),
	// ); err != nil {
	// 	log.Logger.Panic("create firestore client", zap.Error(err))
	// }
}
