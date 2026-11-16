package controller

import (
	"context"
	"encoding/base64"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/web/arweave/dto"
	telegramDao "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
)

// defaultArweaveContentType is the schema default of ArweaveUpload's
// contentType, used when a client sends an explicit null.
const defaultArweaveContentType = "application/json"

// uploadStore is the part of the telegram upload DAO the resolver uses;
// *telegramDao.Upload implements it and tests substitute a fake.
type uploadStore interface {
	UploadFile(ctx context.Context, cnt []byte, contentType string) (string, error)
	UploadFileWithApikey(ctx context.Context, apikey string, cnt []byte, contentType string) (string, error)
}

// MutationResolver mutation resolver
type MutationResolver struct {
	uploadDao uploadStore
}

// NewMutationResolver builds the Arweave mutation resolver; a nil uploadDao
// disables uploads.
func NewMutationResolver(uploadDao *telegramDao.Upload) *MutationResolver {
	resolver := &MutationResolver{}
	// A nil *Upload stored in the interface would not compare equal to nil.
	if uploadDao != nil {
		resolver.uploadDao = uploadDao
	}
	return resolver
}

// ArweaveUpload uploads a base64 file to Arweave. A caller with a blog JWT
// uploads without billing; otherwise the Authorization bearer is a one-api
// key, which the shared API key authenticator must accept (its caches and
// one-api budgets, charged to the caller's client source) before the upload
// is charged to it, so a junk bearer never reaches one-api's consume.
func (r *MutationResolver) ArweaveUpload(ctx context.Context, fileB64 string, contentType *string) (*dto.UploadResponse, error) {
	if r == nil || r.uploadDao == nil {
		return nil, errors.Errorf("arweave upload disabled: telegram upload service not initialized")
	}

	logger := gmw.GetLogger(ctx)

	authMethod := "jwt"
	var apikey string
	uc := &jwt.UserClaims{}
	if err := auth.Instance.GetUserClaims(ctx, uc); err != nil {
		gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
		if !ok {
			return nil, errors.New("cannot get gin context from standard context")
		}

		header := gctx.GetHeader("Authorization")
		if library.StripBearerPrefix(header) == "" {
			return nil, errors.Wrap(err, "cannot get user claims")
		}
		verified, authErr := mcpauth.FromContextOrHeader(ctx, header)
		switch {
		case authErr == nil:
		case mcpauth.IsUnavailable(authErr):
			return nil, errors.Wrap(authErr, "api key validation is temporarily unavailable; retry shortly")
		default:
			return nil, errors.Wrap(authErr, "cannot get user claims")
		}

		apikey = verified.APIKey
		authMethod = "apikey"
	}

	cnt, err := base64.StdEncoding.DecodeString(fileB64)
	if err != nil {
		return nil, errors.Wrap(err, "decode file b64")
	}

	mediaType := defaultArweaveContentType
	if contentType != nil {
		mediaType = *contentType
	}

	var fileID string
	switch authMethod {
	case "jwt":
		fileID, err = r.uploadDao.UploadFile(ctx, cnt, mediaType)
	case "apikey":
		fileID, err = r.uploadDao.UploadFileWithApikey(ctx, apikey, cnt, mediaType)
	}

	if err != nil {
		return nil, errors.Wrap(err, "upload file")
	}

	logger.Info("upload file to arweave",
		zap.String("user", uc.Subject),
		zap.String("auth_method", authMethod),
		zap.String("file_id", fileID),
		zap.Int("file_size", len(cnt)),
	)
	return &dto.UploadResponse{
		FileID: fileID,
	}, nil
}
