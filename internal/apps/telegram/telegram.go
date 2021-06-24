// Package telegram is telegram server.
//
package telegram

import (
	"context"
	"sync"
	"time"

	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	tb "gopkg.in/tucnak/telebot.v2"

	"laisky-blog-graphql/library/log"
)

// Telegram client
type Telegram struct {
	stop chan struct{}
	bot  *tb.Bot

	db        *MonitorDB
	userStats *sync.Map
}

type userStat struct {
	user  *tb.User
	state string
	lastT time.Time
}

// NewTelegram create new telegram client
func NewTelegram(ctx context.Context, db *MonitorDB, token, api string) (*Telegram, error) {
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
		db:        db,
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

func (b *Telegram) runDefaultHandle() {
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
func (b *Telegram) Stop() {
	b.stop <- struct{}{}
}

func (b *Telegram) dispatcher(msg *tb.Message) {
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
func (b *Telegram) PleaseRetry(sender *tb.User, msg string) {
	log.Logger.Warn("unknown msg", zap.String("msg", msg))
	if _, err := b.bot.Send(sender, "[Error] unknown msg, please retry"); err != nil {
		log.Logger.Error("send msg by telegram", zap.Error(err))
	}
}

func (b *Telegram) SendMsgToUser(uid int, msg string) (err error) {
	_, err = b.bot.Send(&tb.User{ID: uid}, msg)
	return err
}
