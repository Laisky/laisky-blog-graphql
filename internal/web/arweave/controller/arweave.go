package controller

import (
	"context"
	"encoding/base64"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v5"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/laisky-blog-graphql/internal/web/arweave/dto"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	"github.com/Laisky/laisky-blog-graphql/library/db/arweave"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
	"github.com/Laisky/zap"
)

// MutationResolver mutation resolver
type MutationResolver struct {
}

func NewMutationResolver() *MutationResolver {
	return &MutationResolver{}
}

func (r *MutationResolver) ArweaveUpload(ctx context.Context, fileB64 string, contentType *string) (*dto.UploadResponse, error) {
	logger := gmw.GetLogger(ctx)

	uc := &jwt.UserClaims{}
	if err := auth.Instance.GetUserClaims(ctx, uc); err != nil {
		return nil, errors.Wrap(err, "get user from token")
	}

	storage := arweave.NewArdrive(
		gconfig.S.GetString("settings.arweave.wallet_file"),
		gconfig.S.GetString("settings.arweave.folder_id"),
	)

	cnt, err := base64.StdEncoding.DecodeString(fileB64)
	if err != nil {
		return nil, errors.Wrap(err, "decode base64")
	}

	var opt []arweave.UploadOption
	if contentType != nil {
		opt = append(opt, arweave.WithContentType(*contentType))
	}

	fileID, err := storage.Upload(ctx, cnt, opt...)
	if err != nil {
		return nil, errors.Wrap(err, "upload file to arweave")
	}

	logger.Info("upload file to arweave",
		zap.String("user", uc.Subject),
		zap.String("file_id", fileID),
		zap.Int("file_size", len(cnt)),
	)
	return &dto.UploadResponse{
		FileID: fileID,
	}, nil
}
