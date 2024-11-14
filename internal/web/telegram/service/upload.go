package service

import (
	"context"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v5"
	gutils "github.com/Laisky/go-utils/v4"
	"github.com/Laisky/zap"
	tb "gopkg.in/telebot.v3"
)

func (s *Type) registerUploadHandler() {
	s.bot.Handle("/upload", func(c tb.Context) error {
		m := c.Message()
		s.userStats.Store(m.Sender.ID, &userStat{
			user:  m.Sender,
			state: userWaitUploadFile,
			lastT: gutils.Clock.GetUTCNow(),
		})

		if _, err := s.bot.Send(m.Sender, gutils.Dedent(`
			Please send the file you want to upload. The file size should not exceed 10MB, and only one file or image is supported at a time.
			`)); err != nil {
			return errors.Wrap(err, "send msg")
		}

		return nil
	})
}

func (s *Type) uploadHandler(ctx context.Context, us *userStat, msg *tb.Message) {
	logger := gmw.GetLogger(ctx).With(
		zap.String("user", us.user.Username),
		zap.String("msg", msg.Text),
	)
	logger.Debug("choose upload cmd")

	if msg.Document == nil && msg.Photo == nil {
		s.bot.Send(us.user, "Please send a file or image")
		return
	}

	defer s.userStats.Delete(us.user.ID)

	// FIXME: NOTIMPLEMENTED
	if msg.Document != nil {
		logger.Info("upload document",
			zap.Int64("size", msg.Document.FileSize),
			zap.String("file_name", msg.Document.FileName))
	}

	if msg.Photo != nil {
		logger.Info("upload photo",
			zap.Int64("size", msg.Document.FileSize),
			zap.String("media_type", msg.Photo.MediaType()))
	}
}
