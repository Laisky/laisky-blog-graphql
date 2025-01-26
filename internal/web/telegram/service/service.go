// Package telegram is telegram server.
package service

import (
	"context"
	"sync"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v6"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v5"
	"github.com/Laisky/zap"
	tb "gopkg.in/telebot.v3"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

var (
	_ Interface = new(Telegram)
)

type Interface interface {
	PleaseRetry(sender *tb.User, msg string)
	SendMsgToUser(uid int, msg string) (err error)
	LoadAlertTypesByUser(ctx context.Context, u *model.MonitorUsers) (alerts []*model.AlertTypes, err error)
	LoadAlertTypes(ctx context.Context, cfg *dto.QueryCfg) (alerts []*model.AlertTypes, err error)
	LoadUsers(ctx context.Context, cfg *dto.QueryCfg) (users []*model.MonitorUsers, err error)
	LoadUsersByAlertType(ctx context.Context, a *model.AlertTypes) (users []*model.MonitorUsers, err error)
	ValidateTokenForAlertType(ctx context.Context, token, alertType string) (alert *model.AlertTypes, err error)
}

// Telegram client
type Telegram struct {
	stop      chan struct{}
	bot       *tb.Bot
	userStats *sync.Map

	monitorDao  *dao.Monitor
	telegramDao *dao.Telegram
	UploadDao   *dao.Upload
}

type userStat struct {
	user  *tb.User
	state int
	lastT time.Time
}

// New create new telegram client
func New(ctx context.Context,
	monitorDao *dao.Monitor,
	telegramDao *dao.Telegram,
	uploadDao *dao.Upload,
	token, api string,
) (*Telegram, error) {
	bot, err := tb.NewBot(tb.Settings{
		Token: token,
		URL:   api,
		Poller: &tb.LongPoller{
			Timeout: 1 * time.Second,
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "new telegram bot")
	}

	tel := &Telegram{
		monitorDao:  monitorDao,
		telegramDao: telegramDao,
		UploadDao:   uploadDao,
		stop:        make(chan struct{}),
		bot:         bot,
		userStats:   new(sync.Map),
	}

	if gutils.Contains(gconfig.Shared.GetStringSlice("tasks"), "telegram") {
		// if not enable telegram task,
		// do not consuming telegram events
		go bot.Start()

		tel.registerDefaultHandler(ctx)
		tel.registerMonitorHandler()
		tel.registerArweaveAliasHandler()
		tel.registerNotesSearchHandler()
		tel.registerUploadHandler(ctx)

		go func() {
			select {
			case <-ctx.Done():
			case <-tel.stop:
			}
			bot.Stop()
		}()
	}

	// bot.Send(&tb.User{
	// 	ID: 861999008,
	// }, "yo")

	return tel, nil
}

func (s *Telegram) registerDefaultHandler(ctx context.Context) {
	logger := gmw.GetLogger(ctx)

	handler := func(tbctx tb.Context) error {
		m := tbctx.Message()
		logger.Debug("got message", zap.String("msg", m.Text), zap.Int64("sender", m.Sender.ID))
		if _, ok := s.userStats.Load(m.Sender.ID); ok {
			s.dispatcher(ctx, m)
			return nil
		}

		msg := "You have not enabled any commands yet. Please click the command list button on the left side of the input box to select the command you want to enable."

		if _, err := s.bot.Send(m.Sender, msg); err != nil {
			return errors.Wrapf(err, "send msg to %s", m.Sender.Username)
		}

		return nil
	}

	// start default handler
	s.bot.Handle(tb.OnText, handler)
	s.bot.Handle(tb.OnDocument, handler)
	s.bot.Handle(tb.OnPhoto, handler)
	s.bot.Handle(tb.OnVideo, handler)
}

// Stop stop telegram polling
func (s *Telegram) Stop() {
	s.stop <- struct{}{}
}

func (s *Telegram) dispatcher(ctx context.Context, msg *tb.Message) {
	logger := gmw.GetLogger(ctx)

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	us, ok := s.userStats.Load(msg.Sender.ID)
	if !ok {
		logger.Warn("user not found in userStats", zap.Int64("uid", msg.Sender.ID))
		return
	}

	switch us.(*userStat).state {
	case userWaitChooseMonitorCmd:
		s.monitorHandler(ctx, us.(*userStat), msg)
	case userWaitArweaveAliasCmd:
		s.arweaveAliasHandler(ctx, us.(*userStat), msg)
	case userWaitNotesSearchCmd:
		s.notesSearchHandler(ctx, us.(*userStat), msg)
	case userWaitUploadFile:
		s.uploadHandler(ctx, us.(*userStat), msg)
	case userWaitAuthToUploadFile:
		s.uploadAuthHandler(ctx, us.(*userStat), msg)
	default:
		logger.Warn("unknown msg", zap.Int("user_state", us.(*userStat).state))
		if _, err := s.bot.Send(msg.Sender, "unknown msg, please retry"); err != nil {
			logger.Error("send msg by telegram", zap.Error(err))
		}
	}
}

// PleaseRetry echo retry
func (s *Telegram) PleaseRetry(sender *tb.User, msg string) {
	log.Logger.Warn("unknown msg", zap.String("msg", msg))
	if _, err := s.bot.Send(sender, "[Error] unknown msg, please retry"); err != nil {
		log.Logger.Error("send msg by telegram", zap.Error(err))
	}
}

func (s *Telegram) SendMsgToUser(uid int, msg string) (err error) {
	_, err = s.bot.Send(&tb.User{ID: int64(uid)}, msg,
		&tb.SendOptions{
			ParseMode:             tb.ModeMarkdown,
			DisableWebPagePreview: true,
		},
	)
	return err
}
