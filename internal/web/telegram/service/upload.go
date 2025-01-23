package service

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v6"
	gutils "github.com/Laisky/go-utils/v5"
	glog "github.com/Laisky/go-utils/v5/log"
	"github.com/Laisky/zap"
	tb "gopkg.in/telebot.v3"
)

const MaxUploadFileSize = 10 * 1024 * 1024 // 10MB

func (s *Telegram) registerUploadHandler(ctx context.Context) {
	s.bot.Handle("/upload", func(c tb.Context) (err error) {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()

		s.uploadCmdHandler(ctx, c.Message())
		return nil
	})
}

func (s *Telegram) uploadCmdHandler(ctx context.Context, msg *tb.Message) {
	logger := gmw.GetLogger(ctx)

	// check whether user has permission to upload file
	if hasPerm, err := s.uploadDao.IsUserHasPermToUpload(context.Background(), msg.Sender.ID); err != nil {
		logger.Error("check user has perm to upload", zap.Error(err))
		s.bot.Send(msg.Sender, fmt.Sprintf("failed to check user permission: %s", err.Error()))
		return
	} else if !hasPerm {
		s.userStats.Store(msg.Sender.ID, &userStat{
			user:  msg.Sender,
			state: userWaitAuthToUploadFile,
			lastT: gutils.Clock.GetUTCNow(),
		})

		reply := "Save files to the permanent storage [ARweave network](https://arweave.org/) by sending files in Telegram.\n" +
			"This feature requires payment, so you need to bind to a supported payment account.\n\n" +
			"Please choose the way you want to bind your account. The fee will be deducted from your account each time you upload a file:\n\n" +
			"1. [oneapi apikey](https://wiki.laisky.com/projects/gpt/pay/cn/#page_gpt_pay_cn): Please reply `1 - <YOUR_ONEAPI_APIKEY>`\n"
		_, err = s.bot.Send(msg.Sender, reply, &tb.SendOptions{
			ParseMode:             tb.ModeMarkdown,
			DisableWebPagePreview: true,
		})

		return
	}

	s.userStats.Store(msg.Sender.ID, &userStat{
		user:  msg.Sender,
		state: userWaitUploadFile,
		lastT: gutils.Clock.GetUTCNow(),
	})

	if _, err := s.bot.Send(msg.Sender,
		"Please send the file you want to upload. "+
			"The file size should not exceed 10MB, "+
			"and only one file or image is supported at a time.\n\n"+
			"If you want to reset the bound payment account, please reply `reset`. "+
			"Once you send `reset`, the bound payment account will be cleared, "+
			"and you will need to rebind it to continue using the upload feature. "+
			"However, all files you have successfully uploaded will be permanently stored on the ARweave network and will not be deleted.",
		&tb.SendOptions{
			ParseMode:             tb.ModeMarkdown,
			DisableWebPagePreview: true,
		},
	); err != nil {
		logger.Error("send msg by telegram", zap.Error(err))
		s.bot.Send(msg.Sender, "failed to send msg")
		return
	}
}

func (s *Telegram) uploadAuthHandler(ctx context.Context, us *userStat, msg *tb.Message) {
	logger := gmw.GetLogger(ctx).With(
		zap.String("user", us.user.Username),
		zap.String("msg", msg.Text),
	)
	logger.Debug("choose upload auth cmd")

	errMsg := "Please re-enter like the following format `1 - <YOUR_ONEAPI_APIKEY>`"

	ansers := strings.Split(msg.Text, " - ")
	if len(ansers) != 2 {
		s.bot.Send(us.user, errMsg, &tb.SendOptions{
			ParseMode:             tb.ModeMarkdown,
			DisableWebPagePreview: true,
		})
		return
	}

	var err error
	switch ansers[0] {
	case "1":
		err = s.uploadDao.SaveOneapiUser(ctx, us.user.ID, ansers[1])
	default:
		err = errors.Errorf(errMsg)
	}

	if err != nil {
		logger.Debug("save oneapi user", zap.Error(err))
		s.bot.Send(us.user,
			fmt.Sprintf("failed to save oneapi user: %s", err.Error()),
			&tb.SendOptions{
				ParseMode:             tb.ModeMarkdown,
				DisableWebPagePreview: true,
			},
		)
		return
	}

	s.userStats.Delete(us.user.ID)
	s.bot.Send(us.user, "successfully bind oneapi user")
}

func (s *Telegram) uploadHandler(ctx context.Context, us *userStat, msg *tb.Message) {
	logger := gmw.GetLogger(ctx).With(
		zap.String("user", us.user.Username),
		zap.String("msg", msg.Text),
	)
	logger.Debug("choose upload cmd")

	// reset upload auth
	if strings.ToLower(strings.TrimSpace(msg.Text)) == "reset" {
		s.uploadDao.ResetUser(ctx, us.user.ID)
		s.uploadCmdHandler(ctx, msg)
		return
	}

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

	fileID, err = s.uploadDao.UploadFile(ctx,
		msg.Sender.ID, cnt, contentType,
	)
	if err != nil {
		return fileID, errors.Wrap(err, "upload file")
	}

	logger.Info("upload file success", zap.String("file_id", fileID))
	return fileID, nil
}
