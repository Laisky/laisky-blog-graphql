package arweave

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"regexp"

	gutils "github.com/Laisky/go-utils/v4"
	"github.com/Laisky/laisky-blog-graphql/internal/web/arweave/dto"
	"github.com/pkg/errors"
)

var jsonReg = regexp.MustCompile(`(?s:(\{.*\}))`)

type Ardrive struct {
	walletPath string
	folder     string
}

func NewArdrive(walletPath string, folder string) *Ardrive {
	return &Ardrive{walletPath: walletPath, folder: folder}
}

func (a *Ardrive) Upload(ctx context.Context,
	data []byte, opts ...UploadOption) (fileID string, err error) {
	opt, err := new(uploadOption).apply(opts...)
	if err != nil {
		return "", err
	}

	bin, err := exec.LookPath("ardrive")
	if err != nil {
		return "", errors.Wrap(err, "look path for ardrive")
	}

	// write file content to temp file
	tmpFile, err := os.CreateTemp("", "arweave-upload-*")
	if err != nil {
		return "", errors.Wrap(err, "create temp file")
	}
	defer os.Remove(tmpFile.Name())

	if _, err = io.Copy(tmpFile, bytes.NewReader(data)); err != nil {
		return "", errors.Wrap(err, "write data to temp file")
	}

	if err = tmpFile.Close(); err != nil {
		return "", errors.Wrap(err, "close file")
	}

	// upload file
	stdout, err := gutils.RunCMD(ctx, bin,
		"upload-file",
		"--content-type", opt.contentType,
		"--local-path", tmpFile.Name(),
		"--parent-folder-id", a.folder,
		"-w", a.walletPath,
	)
	if err != nil {
		return "", errors.Wrap(err, "upload file")
	}

	matched := jsonReg.FindAllSubmatch(stdout, -1)
	if len(matched) == 0 {
		return "", errors.New("no json output")
	}
	if len(matched[0]) != 2 {
		return "", errors.New("invalid json output")
	}

	resp := new(dto.ArdriveOutput)
	if err = json.NewDecoder(bytes.NewReader(matched[0][1])).Decode(resp); err != nil {
		return "", errors.Wrap(err, "decode ardrive output")
	}

	if len(resp.Created) == 0 {
		return "", errors.New("no file uploaded")
	}

	return resp.Created[0].DataTxId, nil
}
