package arweave

import (
	"context"

	"github.com/Laisky/errors/v2"
	"github.com/everFinance/goar"
	"github.com/everFinance/goar/types"
)

const arweaveGateway = "https://arweave.net"

// Ardrive uploads data to Arweave using the goar SDK.
type Ardrive struct {
	wallet *goar.Wallet
}

// NewArdrive creates a new Ardrive uploader.
//
// walletPath is the path to the Arweave JWK wallet file.
// folder is kept for backward compatibility but no longer used,
// because we upload raw data transactions directly instead of
// going through the ArFS folder abstraction.
func NewArdrive(walletPath string, folder string) *Ardrive {
	w, err := goar.NewWalletFromPath(walletPath, arweaveGateway)
	if err != nil {
		// Defer the error — Upload will fail with a clear message.
		return &Ardrive{}
	}
	return &Ardrive{wallet: w}
}

// Upload uploads data to Arweave and returns the transaction ID.
func (a *Ardrive) Upload(ctx context.Context,
	data []byte, opts ...UploadOption) (fileID string, err error) {
	if a.wallet == nil {
		return "", errors.New("arweave wallet not initialized")
	}

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

	tags := []types.Tag{
		{Name: "Content-Type", Value: opt.contentType},
	}

	tx, err := a.wallet.SendData(data, tags)
	if err != nil {
		return "", errors.Wrap(err, "send data to arweave")
	}

	if tx.ID == "" {
		return "", errors.New("arweave returned empty transaction ID")
	}

	return tx.ID, nil
}
