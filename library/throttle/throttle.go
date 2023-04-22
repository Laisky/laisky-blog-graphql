package throttle

import (
	"context"
	"fmt"
	"sync"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v4"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/zap"
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
	totleThrottle  *gutils.Throttle
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

	var tt *gutils.Throttle
	if tt, err = gutils.NewThrottleWithCtx(ctx, &gutils.ThrottleCfg{
		Max:     cfg.TotleBurst,
		NPerSec: cfg.TotleNPerSec,
	}); err != nil {
		return nil, errors.Wrap(err, "create totle throttle")
	}

	t = &TelegramThrottle{
		totleThrottle:  tt,
		titlesThrottle: new(sync.Map),
		cfg:            cfg,
	}
	return t, nil
}

// Allow is allow alertType to push
func (t *TelegramThrottle) Allow(alertType string) (ok bool) {
	var (
		tti interface{}
		tt  *gutils.Throttle
	)
	if tti, ok = t.titlesThrottle.Load(alertType); !ok {
		t.Lock()
		if tti, ok = t.titlesThrottle.Load(alertType); !ok {
			var err error
			if tt, err = gutils.NewThrottleWithCtx(
				context.Background(),
				&gutils.ThrottleCfg{
					Max:     t.cfg.EachTitleBurst,
					NPerSec: t.cfg.EachTitleNPerSec,
				}); err != nil {
				log.Logger.Panic("create new throttle for alertType", zap.Error(err),
					zap.Int("Max", t.cfg.EachTitleBurst),
					zap.Int("NPerSec", t.cfg.EachTitleNPerSec))
			}
			t.titlesThrottle.Store(alertType, tt)
		} else {
			tt = tti.(*gutils.Throttle)
		}
		t.Unlock()
	} else {
		tt = tti.(*gutils.Throttle)
	}

	return tt.Allow() && t.totleThrottle.Allow()
}
