// Package telegram is telegram server.
package service

import (
	"context"
	"sync"
	"time"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config"
	gutils "github.com/Laisky/go-utils/v3"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	tb "gopkg.in/tucnak/telebot.v2"
)

var Instance *Type

func Initialize(ctx context.Context) {
	dao.Initialize(ctx)

	var err error
	if Instance, err = New(
		ctx,
		dao.Instance,
		gconfig.Shared.GetString("settings.telegram.token"),
		gconfig.Shared.GetString("settings.telegram.api"),
	); err != nil {
		log.Logger.Panic("new telegram", zap.Error(err))
	}
}

// Type client
type Type struct {
	stop chan struct{}
	bot  *tb.Bot

	dao       *dao.Type
	userStats *sync.Map
}

type userStat struct {
	user  *tb.User
	state string
	lastT time.Time
}

// New create new telegram client
func New(ctx context.Context, dao *dao.Type, token, api string) (*Type, error) {
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
		dao:       dao,
		stop:      make(chan struct{}),
		bot:       bot,
		userStats: new(sync.Map),
	}

	if gutils.InArray(gconfig.Shared.GetStringSlice("tasks"), "telegram") {
		// if not enable telegram task,
		// do not consuming telegram events
		go bot.Start()
		tel.runDefaultHandle()
		tel.monitorHandler()
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

func (s *Type) runDefaultHandle() {
	// start default handler
	s.bot.Handle(tb.OnText, func(m *tb.Message) {
		log.Logger.Debug("got message", zap.String("msg", m.Text), zap.Int("sender", m.Sender.ID))
		if _, ok := s.userStats.Load(m.Sender.ID); ok {
			s.dispatcher(m)
			return
		}

		if _, err := s.bot.Send(m.Sender, "NotImplement for "+m.Text); err != nil {
			log.Logger.Error("send msg", zap.Error(err), zap.String("to", m.Sender.Username))
		}
	})
}

// Stop stop telegram polling
func (s *Type) Stop() {
	s.stop <- struct{}{}
}

func (s *Type) dispatcher(msg *tb.Message) {
	us, ok := s.userStats.Load(msg.Sender.ID)
	if !ok {
		return
	}

	switch us.(*userStat).state {
	case userWaitChooseMonitorCmd:
		s.chooseMonitor(us.(*userStat), msg)
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
	_, err = s.bot.Send(&tb.User{ID: uid}, msg)
	return err
}
