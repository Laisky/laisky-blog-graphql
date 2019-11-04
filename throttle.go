package laisky_blog_graphql

import (
	"context"
	"fmt"
	"sync"

	"github.com/Laisky/go-utils"
)

// TelegramThrottleCfg configuration for TelegramThrottle
type TelegramThrottleCfg struct {
	TotleNPerSec, TotleBurst         int
	EachTitleNPerSec, EachTitleBurst int
}

// TelegramThrottle throttle for telegram monitor
type TelegramThrottle struct {
	sync.Mutex
	cfg            *TelegramThrottleCfg
	totleThrottle  *utils.Throttle
	titlesThrottle *sync.Map
}

// NewTelegramThrottle create new TelegramThrottle
func NewTelegramThrottle(ctx context.Context, cfg *TelegramThrottleCfg) (t *TelegramThrottle, err error) {
	if cfg.TotleNPerSec <= 0 || cfg.EachTitleNPerSec <= 0 {
		return nil, fmt.Errorf("NPerSec must bigger than 0")
	}
	if cfg.TotleBurst < cfg.TotleNPerSec || cfg.EachTitleBurst < cfg.EachTitleNPerSec {
		return nil, fmt.Errorf("burst must bigger than NPerSec")
	}

	t = &TelegramThrottle{
		totleThrottle: utils.NewThrottleWithCtx(ctx, &utils.ThrottleCfg{
			Max:     cfg.TotleBurst,
			NPerSec: cfg.TotleNPerSec,
		}),
		titlesThrottle: new(sync.Map),
		cfg:            cfg,
	}
	return t, nil
}

// Allow is allow alertType to push
func (t *TelegramThrottle) Allow(alertType string) (ok bool) {
	var (
		tti interface{}
		tt  *utils.Throttle
	)
	if tti, ok = t.titlesThrottle.Load(alertType); !ok {
		t.Lock()
		if tti, ok = t.titlesThrottle.Load(alertType); !ok {
			tt = utils.NewThrottleWithCtx(
				context.Background(),
				&utils.ThrottleCfg{
					Max:     t.cfg.EachTitleBurst,
					NPerSec: t.cfg.EachTitleNPerSec,
				})
			t.titlesThrottle.Store(alertType, tt)
		} else {
			tt = tti.(*utils.Throttle)
		}
		t.Unlock()
	} else {
		tt = tti.(*utils.Throttle)
	}

	return tt.Allow() && t.totleThrottle.Allow()
}
