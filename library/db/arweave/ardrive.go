package arweave

import (
	"context"
	"strconv"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"
	"github.com/permadao/goar"
	"github.com/permadao/goar/schema"
	"github.com/permadao/goar/utils"
)

const (
	arweaveGateway = "https://arweave.net"
	// committedUploadTimeout bounds an upload after its signed transaction is
	// sent. A node drops a pending data root after about 30 minutes, so
	// chunks that are still unsent by then may no longer be accepted.
	committedUploadTimeout = 30 * time.Minute
)

// Ardrive uploads data to Arweave as signed format-2 data transactions.
type Ardrive struct {
	signer        *goar.Signer
	gateway       *gatewayClient
	commitTimeout time.Duration
}

// NewArdrive creates a new Ardrive uploader.
//
// walletPath is the path to the Arweave JWK wallet file.
// folder is kept for backward compatibility but no longer used,
// because we upload raw data transactions directly instead of
// going through the ArFS folder abstraction.
func NewArdrive(walletPath string, folder string) *Ardrive {
	return newArdrive(walletPath, arweaveGateway)
}

// newArdrive binds the wallet to gatewayURL so tests can exercise the real
// upload path against a local gateway.
func newArdrive(walletPath, gatewayURL string) *Ardrive {
	signer, err := goar.NewSignerFromPath(walletPath)
	if err != nil {
		// Defer the error — Upload will fail with a clear message.
		return &Ardrive{}
	}
	return &Ardrive{
		signer:        signer,
		gateway:       newGatewayClient(gatewayURL),
		commitTimeout: committedUploadTimeout,
	}
}

// Upload uploads data to Arweave and returns the transaction ID.
//
// The ID means the gateway accepted the transaction and every chunk, not
// that the transaction is confirmed on chain.
//
// Canceling ctx stops the upload until its signed transaction is sent.
// Sending it commits the reward, which is spent once the transaction is
// mined whether or not its data arrives, so from then on Upload finishes the
// chunks under a context that ignores ctx's cancellation and deadline and is
// bounded by committedUploadTimeout instead. Each gateway request has its own
// timeout, and transient gateway failures are retried.
func (a *Ardrive) Upload(ctx context.Context,
	data []byte, opts ...UploadOption) (fileID string, err error) {
	if a.signer == nil {
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

	fileID, err = a.send(ctx, data, []schema.Tag{
		{Name: "Content-Type", Value: opt.contentType},
	})
	if err != nil {
		return "", errors.Wrap(err, "send data to arweave")
	}
	return fileID, nil
}

// send signs data as a transaction, posts it and then posts its chunks.
func (a *Ardrive) send(ctx context.Context, data []byte, tags []schema.Tag) (string, error) {
	logger := gmw.GetLogger(ctx)
	if err := ctx.Err(); err != nil {
		return "", errors.Wrap(err, "upload canceled before it started")
	}

	tx, err := a.signTransaction(ctx, data, tags)
	if err != nil {
		return "", errors.WithStack(err)
	}
	if err = ctx.Err(); err != nil {
		return "", errors.Wrap(err, "upload canceled before the transaction was sent")
	}

	// Past this point the reward is committed: finish the upload even if the
	// caller stops waiting, within its own bound.
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.commitTimeout)
	defer cancel()

	if err = a.gateway.postTransaction(commitCtx, tx); err != nil {
		return "", errors.Wrapf(err, "send transaction %s", tx.ID)
	}
	total := len(tx.Chunks.Chunks)
	for i := range total {
		chunk, err := utils.GetChunk(*tx, i, data)
		if err != nil {
			return "", errors.Wrapf(err, "prepare chunk %d/%d of transaction %s", i+1, total, tx.ID)
		}
		if err = a.gateway.postChunk(commitCtx, chunk); err != nil {
			return "", errors.Wrapf(err, "upload chunk %d/%d of transaction %s", i+1, total, tx.ID)
		}
	}

	if ctx.Err() != nil {
		logger.Info("finished committed arweave upload after the caller stopped waiting",
			zap.String("tx_id", tx.ID), zap.Int("chunks", total))
	}
	logger.Debug("uploaded arweave transaction",
		zap.String("tx_id", tx.ID), zap.Int("data_size", len(data)), zap.Int("chunks", total))
	return tx.ID, nil
}

// signTransaction quotes, anchors and signs a format-2 data transaction.
// Its chunks and data root are computed from data, which is not embedded:
// the header is posted without data and the chunks follow it.
func (a *Ardrive) signTransaction(ctx context.Context, data []byte, tags []schema.Tag) (*schema.Transaction, error) {
	reward, err := a.gateway.price(ctx, len(data))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	anchor, err := a.gateway.anchor(ctx)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	tx := &schema.Transaction{
		Format:   2,
		LastTx:   anchor,
		Owner:    a.signer.Owner(),
		Tags:     utils.TagsEncode(tags),
		Target:   "",
		Quantity: "0",
		DataSize: strconv.Itoa(len(data)),
		Reward:   reward,
	}
	if err = utils.PrepareChunks(tx, data, len(data)); err != nil {
		return nil, errors.Wrap(err, "prepare chunks")
	}
	if err = a.signer.SignTx(tx); err != nil {
		return nil, errors.Wrap(err, "sign transaction")
	}
	return tx, nil
}
