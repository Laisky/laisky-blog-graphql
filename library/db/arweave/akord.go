// Package arweave is a wrapper for the Arweave client.
package arweave

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
)

// AkrodAPI akord api
const AkrodAPI = "https://api.akord.com/files"

// Akord akord uploader
type Akord struct {
	apis []string
}

// AkrodUploadFileResp response of akord upload file
type AkrodUploadFileResp struct {
	Tx struct {
		Id string `json:"id"`
	} `json:"tx"`
}

// NewAkrod create a new akord uploader
func NewAkrod(apis []string) *Akord {
	return &Akord{apis: apis}
}

// Upload upload data to akord
func (a *Akord) Upload(ctx context.Context,
	data []byte, opts ...UploadOption) (fileID string, err error) {
	opt, err := new(uploadOption).apply(opts...)
	if err != nil {
		return "", err
	}

	headers := map[string]string{
		"Accept":       "application/json",
		"Api-Key":      gutils.RandomChoice(a.apis, 1)[0],
		"Content-Type": opt.contentType,
	}

	reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Second)
	defer reqCancel()

	req, err := http.NewRequestWithContext(reqCtx,
		http.MethodPost, AkrodAPI, bytes.NewReader(data))
	if err != nil {
		return "", errors.Wrap(err, "post file to akord")
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "post file to akord")
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "read response body")
	}

	if resp.StatusCode != http.StatusCreated {
		return "", errors.Errorf("[%d] %s", resp.StatusCode, string(respBody))
	}

	responseData := new(AkrodUploadFileResp)
	err = json.Unmarshal(respBody, responseData)
	if err != nil {
		return "", errors.Wrap(err, "unmarshal response")
	}

	return responseData.Tx.Id, nil
}
