package service

import (
	"context"
	"fmt"
	"io"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v5"
	gutils "github.com/Laisky/go-utils/v4"
	glog "github.com/Laisky/go-utils/v4/log"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/library/db/arweave"
	"github.com/Laisky/zap"
	tb "gopkg.in/telebot.v3"
)

const MaxUploadFileSize = 10 * 1024 * 1024 // 10MB

func (s *Telegram) registerUploadHandler() {
	s.bot.Handle("/upload", func(c tb.Context) (err error) {
		m := c.Message()

		if err = s.userHasPermissionToUpload(m); err != nil {
			return errors.Wrap(err, "check user state")
		}

		s.userStats.Store(m.Sender.ID, &userStat{
			user:  m.Sender,
			state: userWaitUploadFile,
			lastT: gutils.Clock.GetUTCNow(),
		})

		if _, err = s.bot.Send(m.Sender, gutils.Dedent(`
			Please send the file you want to upload. The file size should not exceed 10MB, and only one file or image is supported at a time.
			`)); err != nil {
			return errors.Wrap(err, "send msg")
		}

		return nil
	})
}

func (s *Telegram) userHasPermissionToUpload(msg *tb.Message) error {
	// bypass admin
	if msg.Sender.ID == model.TelegramAdminUID {
		return nil
	}

	return errors.Errorf("only admin can upload file")
}

func (s *Telegram) uploadHandler(ctx context.Context, us *userStat, msg *tb.Message) {
	logger := gmw.GetLogger(ctx).With(
		zap.String("user", us.user.Username),
		zap.String("msg", msg.Text),
	)
	logger.Debug("choose upload cmd")

	if msg.Document == nil && msg.Photo == nil {
		s.bot.Send(us.user, "please send a file or image to upload")
		return
	}

	fileID, err := s._handleUserUploadedFile(ctx, logger, msg)
	if err != nil {
		logger.Error("handle user uploaded file", zap.Error(err))
		s.bot.Send(us.user, fmt.Sprintf("failed to upload file: %s", err.Error()))
	}

	sendMsg := fmt.Sprintf("successfully uploaded file, You may need to wait for a minute for it to take effect.\n[https://ario.laisky.com/%s](https://ario.laisky.com/%s)", fileID, fileID)

	_, err = s.bot.Send(us.user, sendMsg, &tb.SendOptions{
		ParseMode: tb.ModeMarkdown,
	})
	if err != nil {
		logger.Error("send msg by telegram", zap.Error(err))
	}
}

// _handleUserUploadedFile handle user uploaded file
func (s *Telegram) _handleUserUploadedFile(ctx context.Context,
	logger glog.Logger, msg *tb.Message,
) (fileID string, err error) {
	var contentType string

	var telFp *tb.File
	if msg.Document != nil {
		logger.Debug("receive document",
			zap.Int64("size", msg.Document.FileSize),
			zap.String("type", msg.Document.MIME),
			zap.String("file_name", msg.Document.FileName))
		if msg.Document.FileSize > MaxUploadFileSize {
			return fileID, errors.Errorf("file size exceeds limit %d", MaxUploadFileSize)
		}
		telFp = &msg.Document.File
		contentType = msg.Document.MIME
	}

	if msg.Photo != nil {
		logger.Debug("receive photo",
			zap.Int64("size", msg.Photo.FileSize),
			zap.String("media_type", msg.Photo.MediaType()))
		if msg.Photo.FileSize > MaxUploadFileSize {
			return fileID, errors.Errorf("file size exceeds limit %d", MaxUploadFileSize)
		}
		telFp = &msg.Photo.File
		contentType = "image/png"
	}

	if telFp == nil {
		return fileID, errors.New("no file found")
	}

	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// download file
	fp, err := s.bot.File(telFp)
	if err != nil {
		return fileID, errors.Wrap(err, "download file from telegram")
	}

	cnt, err := io.ReadAll(fp)
	if err != nil {
		return fileID, errors.Wrap(err, "read file content")
	}

	fileID, err = s.uploadDap.UploadFile(ctx,
		msg.Sender.ID, cnt,
		arweave.WithContentType(contentType),
	)
	if err != nil {
		return fileID, errors.Wrap(err, "upload file")
	}

	logger.Info("upload file success", zap.String("file_id", fileID))
	return fileID, nil
}
