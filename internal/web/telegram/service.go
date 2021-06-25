// Package telegram is telegram server.
//
package telegram

import (
	"context"
	"sync"
	"time"

	"laisky-blog-graphql/internal/web/telegram/db"
	"laisky-blog-graphql/library/log"

	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	tb "gopkg.in/tucnak/telebot.v2"
)

// Service client
type Service struct {
	stop chan struct{}
	bot  *tb.Bot

	*db.DB
	userStats *sync.Map
}

type userStat struct {
	user  *tb.User
	state string
	lastT time.Time
}

// NewService create new telegram client
func NewService(ctx context.Context, db *db.DB, token, api string) (*Service, error) {
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

	tel := &Service{
		DB:        db,
		stop:      make(chan struct{}),
		bot:       bot,
		userStats: new(sync.Map),
	}
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

	// bot.Send(&tb.User{
	// 	ID: 861999008,
	// }, "yo")

	return tel, nil
}

func (b *Service) runDefaultHandle() {
	// start default handler
	b.bot.Handle(tb.OnText, func(m *tb.Message) {
		log.Logger.Debug("got message", zap.String("msg", m.Text), zap.Int("sender", m.Sender.ID))
		if _, ok := b.userStats.Load(m.Sender.ID); ok {
			b.dispatcher(m)
			return
		}

		if _, err := b.bot.Send(m.Sender, "NotImplement for "+m.Text); err != nil {
			log.Logger.Error("send msg", zap.Error(err), zap.String("to", m.Sender.Username))
		}
	})
}

// Stop stop telegram polling
func (b *Service) Stop() {
	b.stop <- struct{}{}
}

func (b *Service) dispatcher(msg *tb.Message) {
	us, ok := b.userStats.Load(msg.Sender.ID)
	if !ok {
		return
	}

	switch us.(*userStat).state {
	case userWaitChooseMonitorCmd:
		b.chooseMonitor(us.(*userStat), msg)
	default:
		log.Logger.Warn("unknown msg")
		if _, err := b.bot.Send(msg.Sender, "unknown msg, please retry"); err != nil {
			log.Logger.Error("send msg by telegram", zap.Error(err))
		}
	}
}

// PleaseRetry echo retry
func (b *Service) PleaseRetry(sender *tb.User, msg string) {
	log.Logger.Warn("unknown msg", zap.String("msg", msg))
	if _, err := b.bot.Send(sender, "[Error] unknown msg, please retry"); err != nil {
		log.Logger.Error("send msg by telegram", zap.Error(err))
	}
}

func (b *Service) SendMsgToUser(uid int, msg string) (err error) {
	_, err = b.bot.Send(&tb.User{ID: uid}, msg)
	return err
}
