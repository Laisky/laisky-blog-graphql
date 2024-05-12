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
	gutils "github.com/Laisky/go-utils/v4"
)

// AkrodAPI akrod api
const AkrodAPI = "https://api.akord.com/files"

// Akrod akrod uploader
type Akrod struct {
	apis []string
}

// AkrodUploadFileResp response of akrod upload file
type AkrodUploadFileResp struct {
	Tx struct {
		Id string `json:"id"`
	} `json:"tx"`
}

// NewAkrod create a new akrod uploader
func NewAkrod(apis []string) *Akrod {
	return &Akrod{apis: apis}
}

type uploadOption struct {
	contentType string
}

type UploadOption func(*uploadOption) error

func (o *uploadOption) apply(opts ...UploadOption) (*uploadOption, error) {
	// fill default
	o.contentType = "text/plain"

	// apply opts
	for _, opt := range opts {
		if err := opt(o); err != nil {
			return nil, err
		}
	}

	return o, nil
}

// Upload upload data to akrod
func (a *Akrod) Upload(ctx context.Context,
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

	reqBody, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Second)
	defer reqCancel()

	req, err := http.NewRequestWithContext(reqCtx,
		http.MethodPost, AkrodAPI, bytes.NewBuffer(reqBody))
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
