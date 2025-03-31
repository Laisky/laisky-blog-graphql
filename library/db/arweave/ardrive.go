package arweave

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"regexp"

	gutils "github.com/Laisky/go-utils/v5"
	"github.com/pkg/errors"

	"github.com/Laisky/laisky-blog-graphql/internal/web/arweave/dto"
)

var jsonReg = regexp.MustCompile(`(?s:(\{.*\}))`)

// Ardrive is a struct that provides methods to interact with the Ardrive CLI tool for uploading files.
type Ardrive struct {
	walletPath string
	folder     string
}

// NewArdrive creates a new instance of Ardrive with the specified wallet path and folder.
func NewArdrive(walletPath string, folder string) *Ardrive {
	return &Ardrive{walletPath: walletPath, folder: folder}
}

// Upload uploads a file to Ardrive using the `ardrive` CLI tool.
func (a *Ardrive) Upload(ctx context.Context,
	data []byte, opts ...UploadOption) (fileID string, err error) {
	opt, err := new(uploadOption).apply(opts...)
	if err != nil {
		return "", errors.Wrap(err, "apply upload options")
	}

	if opt.gzip {
		data, err = CompressData(data)
		if err != nil {
			return "", errors.Wrap(err, "compress data")
		}
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
		"--replace",
	)
	if err != nil {
		return "", errors.Wrap(err, "upload file")
	}

	matched := jsonReg.FindAllSubmatch(stdout, -1)
	if len(matched) == 0 {
		return "", errors.Errorf("no json output, got: %s", string(stdout))
	}
	if len(matched[0]) != 2 {
		return "", errors.Errorf("invalid json output, got %s", string(stdout))
	}

	resp := new(dto.ArdriveOutput)
	if err = json.NewDecoder(bytes.NewReader(matched[0][1])).Decode(resp); err != nil {
		return "", errors.Wrapf(err, "failed to decode ardrive output, , got %s", string(stdout))
	}

	if len(resp.Created) == 0 {
		return "", errors.Errorf("no file uploaded, got %s", string(stdout))
	}

	return resp.Created[0].DataTxId, nil
}
