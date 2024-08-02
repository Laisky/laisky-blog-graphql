// Package telegram is telegram server.
package service

import (
	"context"
	"sync"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v4"
	"github.com/Laisky/zap"
	tb "gopkg.in/telebot.v3"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

var (
	_ Interface = new(Type)
)

type Interface interface {
	PleaseRetry(sender *tb.User, msg string)
	SendMsgToUser(uid int, msg string) (err error)
	LoadAlertTypesByUser(ctx context.Context, u *model.Users) (alerts []*model.AlertTypes, err error)
	LoadAlertTypes(ctx context.Context, cfg *dto.QueryCfg) (alerts []*model.AlertTypes, err error)
	LoadUsers(ctx context.Context, cfg *dto.QueryCfg) (users []*model.Users, err error)
	LoadUsersByAlertType(ctx context.Context, a *model.AlertTypes) (users []*model.Users, err error)
	ValidateTokenForAlertType(ctx context.Context, token, alertType string) (alert *model.AlertTypes, err error)
}

// Type client
type Type struct {
	stop chan struct{}
	bot  *tb.Bot

	monitorDao  *dao.Monitor
	telegramDao *dao.Telegram
	userStats   *sync.Map
}

type userStat struct {
	user  *tb.User
	state string
	lastT time.Time
}

// New create new telegram client
func New(ctx context.Context,
	monitorDao *dao.Monitor,
	telegramDao *dao.Telegram,
	token, api string) (*Type, error) {
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

	tel := &Type{
		monitorDao:  monitorDao,
		telegramDao: telegramDao,
		stop:        make(chan struct{}),
		bot:         bot,
		userStats:   new(sync.Map),
	}

	if gutils.Contains(gconfig.Shared.GetStringSlice("tasks"), "telegram") {
		// if not enable telegram task,
		// do not consuming telegram events
		go bot.Start()
		tel.runDefaultHandle(ctx)
		tel.monitorHandler()
		tel.arweaveAliasHandler()
		tel.notesSearchHandler()
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

func (s *Type) runDefaultHandle(ctx context.Context) {
	// start default handler
	s.bot.Handle(tb.OnText, func(tbctx tb.Context) error {
		m := tbctx.Message()
		log.Logger.Debug("got message", zap.String("msg", m.Text), zap.Int64("sender", m.Sender.ID))
		if _, ok := s.userStats.Load(m.Sender.ID); ok {
			s.dispatcher(ctx, m)
			return nil
		}

		if _, err := s.bot.Send(m.Sender, "NotImplement for "+m.Text); err != nil {
			return errors.Wrapf(err, "send msg to %s", m.Sender.Username)
		}

		return nil
	})
}

// Stop stop telegram polling
func (s *Type) Stop() {
	s.stop <- struct{}{}
}

func (s *Type) dispatcher(ctx context.Context, msg *tb.Message) {
	us, ok := s.userStats.Load(msg.Sender.ID)
	if !ok {
		return
	}

	switch us.(*userStat).state {
	case userWaitChooseMonitorCmd:
		s.chooseMonitor(ctx, us.(*userStat), msg)
	case userWaitArweaveAliasCmd:
		s.arweaveAliasDispatcher(ctx, us.(*userStat), msg)
	case userWaitNotesSearchCmd:
		s.notesSearchDispatcher(ctx, us.(*userStat), msg)
	default:
		log.Logger.Warn("unknown msg")
		if _, err := s.bot.Send(msg.Sender, "unknown msg, please retry"); err != nil {
			log.Logger.Error("send msg by telegram", zap.Error(err))
		}
	}
}

// PleaseRetry echo retry
func (s *Type) PleaseRetry(sender *tb.User, msg string) {
	log.Logger.Warn("unknown msg", zap.String("msg", msg))
	if _, err := s.bot.Send(sender, "[Error] unknown msg, please retry"); err != nil {
		log.Logger.Error("send msg by telegram", zap.Error(err))
	}
}

func (s *Type) SendMsgToUser(uid int, msg string) (err error) {
	_, err = s.bot.Send(&tb.User{ID: int64(uid)}, msg)
	return err
}
