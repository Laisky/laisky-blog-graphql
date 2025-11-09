package arweave

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"

	gutils "github.com/Laisky/go-utils/v6"
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

	// Create temp file with explicit permissions
	tmpFile, err := os.CreateTemp("", "arweave-upload-*.tmp")
	if err != nil {
		return "", errors.Wrap(err, "create temp file")
	}
	tmpFilePath := tmpFile.Name()
	defer func() {
		tmpFile.Close() // Ensure file is closed even if not explicitly closed above
		os.Remove(tmpFilePath)
	}()

	// Set file permissions explicitly
	if err = tmpFile.Chmod(0644); err != nil {
		return "", errors.Wrap(err, "set file permissions")
	}

	// Write data to temp file
	if _, err = tmpFile.Write(data); err != nil {
		return "", errors.Wrap(err, "write data to temp file")
	}

	// Close the file handle before running the external command
	if err = tmpFile.Close(); err != nil {
		return "", errors.Wrap(err, "close temp file")
	}

	// Verify file exists and is readable before running ardrive
	if _, err = os.Stat(tmpFilePath); err != nil {
		return "", errors.Wrap(err, "temp file not accessible")
	}

	// upload file with explicit environment to avoid TLS warnings
	envs := os.Environ()
	envs = append(envs, "NODE_TLS_REJECT_UNAUTHORIZED=1") // Override the insecure setting

	stdout, err := gutils.RunCMDWithEnv(ctx, bin,
		[]string{
			"upload-file",
			"--content-type", opt.contentType,
			"--local-path", tmpFilePath,
			"--parent-folder-id", a.folder,
			"-w", a.walletPath,
			"--replace",
		},
		envs,
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

	if resp.Created[0].DataTxId == "" {
		return "", errors.Errorf("no data tx id in response, got %s", string(stdout))
	}

	return resp.Created[0].DataTxId, nil
}
