package controller

import (
	"context"
	"encoding/base64"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v6"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/web/arweave/dto"
	telegramDao "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
)

// MutationResolver mutation resolver
type MutationResolver struct {
	uploadDao *telegramDao.Upload
}

func NewMutationResolver(uploadDao *telegramDao.Upload) *MutationResolver {
	return &MutationResolver{
		uploadDao: uploadDao,
	}
}

func (r *MutationResolver) ArweaveUpload(ctx context.Context, fileB64 string, contentType *string) (*dto.UploadResponse, error) {
	logger := gmw.GetLogger(ctx)

	authMethod := "jwt"
	var apikey string
	uc := &jwt.UserClaims{}
	if err := auth.Instance.GetUserClaims(ctx, uc); err != nil {
		gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
		if !ok {
			return nil, errors.New("cannot get gin context from standard context")
		}

		apikey = library.StripBearerPrefix(gctx.GetHeader("Authorization"))
		if apikey == "" {
			return nil, errors.Wrap(err, "cannot get user claims")
		}

		authMethod = "apikey"
	}

	cnt, err := base64.StdEncoding.DecodeString(fileB64)
	if err != nil {
		return nil, errors.Wrap(err, "decode file b64")
	}

	var fileID string
	switch authMethod {
	case "jwt":
		fileID, err = r.uploadDao.UploadFile(ctx, cnt, *contentType)
	case "apikey":
		fileID, err = r.uploadDao.UploadFileWithApikey(ctx, apikey, cnt, *contentType)
	}

	if err != nil {
		return nil, errors.Wrap(err, "upload file")
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
