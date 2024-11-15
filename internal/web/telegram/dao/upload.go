// Package dao is a data access object for telegram Upload.
package dao

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	mongoLib "go.mongodb.org/mongo-driver/mongo"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v5"
	"github.com/Laisky/laisky-blog-graphql/library/db/arweave"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	"github.com/Laisky/zap"
)

const (
	colUploadUsers = "upload_users"
	colUploadFiles = "upload_files"
)

// Upload db
type Upload struct {
	db mongo.DB
	ar *arweave.Ardrive
}

// NewUpload create new DB
func NewUpload(
	db mongo.DB,
	ar *arweave.Ardrive,
) *Upload {
	return &Upload{
		db: db,
		ar: ar,
	}
}

func (d *Upload) GetUsersCol() *mongoLib.Collection {
	return d.db.GetCol(colUploadUsers)
}
func (d *Upload) GetFilesCol() *mongoLib.Collection {
	return d.db.GetCol(colUploadFiles)
}

func (d *Upload) UploadFile(ctx context.Context, uid int64, cnt []byte, opts ...arweave.UploadOption) (fileID string, err error) {
	logger := gmw.GetLogger(ctx)

	fileID, err = d.ar.Upload(ctx, cnt, opts...)
	if err != nil {
		return "", errors.WithStack(err)
	}

	// save file info
	_, err = d.db.GetCol(colUploadFiles).
		InsertOne(ctx, bson.M{
			"created_at":   time.Now(),
			"file_id":      fileID,
			"file_size":    len(cnt),
			"telegram_uid": uid,
		})
	if err != nil {
		logger.Error("save file info", zap.Error(err))
	}

	return fileID, nil
}
