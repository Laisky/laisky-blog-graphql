package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v5"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v4"
	"github.com/Laisky/laisky-blog-graphql/internal/web/arweave/dto"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
	"github.com/Laisky/zap"
)

// MutationResolver mutation resolver
type MutationResolver struct {
}

func NewMutationResolver() *MutationResolver {
	return &MutationResolver{}
}

var jsonReg = regexp.MustCompile(`(?s:(\{.*\}))`)

func (r *MutationResolver) ArweaveUpload(ctx context.Context, fileB64 string) (*dto.UploadResponse, error) {
	logger := gmw.GetLogger(ctx)

	uc := &jwt.UserClaims{}
	if err := auth.Instance.GetUserClaims(ctx, uc); err != nil {
		return nil, errors.Wrap(err, "get user from token")
	}

	bin, err := exec.LookPath("ardrive")
	if err != nil {
		return nil, errors.Wrap(err, "look path for ardrive")
	}

	parentFolderId := gconfig.Shared.GetString("settings.arweave.folder_id")
	walletPath := gconfig.Shared.GetString("settings.arweave.wallet_file")

	// write file content to temp file
	tmpFile, err := os.CreateTemp("", "arweave-upload-*")
	if err != nil {
		return nil, errors.Wrap(err, "create temp file")
	}
	defer os.Remove(tmpFile.Name())

	// decode and write file by chunk
	dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(fileB64))
	written, err := io.Copy(tmpFile, dec)
	if err != nil {
		return nil, errors.Wrap(err, "write file")
	}

	if err = tmpFile.Close(); err != nil {
		return nil, errors.Wrap(err, "close file")
	}

	// upload file
	stdout, err := gutils.RunCMD(ctx, bin,
		"upload-file",
		"--local-path", tmpFile.Name(),
		"--parent-folder-id", parentFolderId,
		"-w", walletPath,
	)
	if err != nil {
		return nil, errors.Wrap(err, "upload file")
	}

	matched := jsonReg.FindAllSubmatch(stdout, -1)
	if len(matched) == 0 {
		return nil, errors.New("no json output")
	}
	if len(matched[0]) != 2 {
		return nil, errors.New("invalid json output")
	}

	resp := new(dto.ArdriveOutput)
	if err = json.NewDecoder(bytes.NewReader(matched[0][1])).Decode(resp); err != nil {
		return nil, errors.Wrap(err, "decode ardrive output")
	}

	if len(resp.Created) == 0 {
		return nil, errors.New("no file uploaded")
	}

	logger.Info("upload file to arweave",
		zap.String("user", uc.Subject),
		zap.Int64("file_size", written),
	)
	return &dto.UploadResponse{
		FileID: resp.Created[0].DataTxId,
	}, nil
}
