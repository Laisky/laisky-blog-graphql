// Package telegram is telegram server.
//
package telegram

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Laisky/zap"

	"github.com/Laisky/go-utils"

	"github.com/pkg/errors"

	tb "gopkg.in/tucnak/telebot.v2"
)

// GetTelegramCli return telegram client
func GetTelegramCli() *Telegram {
	switch r := utils.Settings.Get(TelegramCliKey).(type) {
	case *Telegram:
		return r
	case nil:
		return nil
	default:
		utils.Logger.Panic("unknow type of telegram cli", zap.String("val", fmt.Sprint(r)))
	}

	return nil
}

// Telegram client
type Telegram struct {
	stop chan struct{}
	bot  *tb.Bot

	db        *MonitorDB
	userStats *sync.Map
}

const (
	userWaitChooseMonitorCmd = "userWaitChooseMonitorCmd"
)

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
			Timeout: 5 * time.Second,
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
		utils.Logger.Debug("got message", zap.String("msg", m.Text), zap.Int("sender", m.Sender.ID))
		if _, ok := b.userStats.Load(m.Sender.ID); ok {
			b.dispatcher(m)
			return
		}

		if _, err := b.bot.Send(m.Sender, "NotImplement for "+m.Text); err != nil {
			utils.Logger.Error("send msg", zap.Error(err), zap.String("to", m.Sender.Username))
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
		utils.Logger.Warn("unknown msg")
		b.bot.Send(msg.Sender, "unknown msg, please retry")
	}
}

// PleaseRetry echo retry
func (b *Telegram) PleaseRetry(sender *tb.User, msg string) {
	utils.Logger.Warn("unknown msg", zap.String("msg", msg))
	b.bot.Send(sender, "[Error] unknown msg, please retry")
}

func (b *Telegram) SendMsgToUser(uid int, msg string) (err error) {
	_, err = b.bot.Send(&tb.User{
		ID: uid,
	}, msg)
	return err
}
