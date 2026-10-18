package log

import (
	"os"
	"strings"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
)

// privateOTLPOption prepares an opted-in output branch without reading credentials at logger creation.
// Disabled loggers retain their original Core and do not execute extra field callbacks.
func privateOTLPOption() zap.Option {
	return zap.WrapCore(func(base zapcore.Core) zapcore.Core {
		if _, alreadyPrepared := base.(*otlpContextCore); alreadyPrepared {
			return base
		}
		if strings.TrimSpace(os.Getenv("APP_LOG_OTLP_ENDPOINT")) == "" && activeOTLP.Load() == nil {
			return base
		}
		bound := newOTLPObjectEncoder()
		bound.addFields(base.Fields())
		return newOTLPContextCore(base, bound)
	})
}

// otlpContextCore snapshots bound fields while retaining the fork's exact writer-acceptance gate.
type otlpContextCore struct {
	zapcore.Core
	base  zapcore.Core
	bound *otlpObjectEncoder
}

// newOTLPContextCore registers one private callback without changing the base's writes or decisions.
func newOTLPContextCore(base zapcore.Core, bound *otlpObjectEncoder) *otlpContextCore {
	c := &otlpContextCore{base: base, bound: bound}
	boundCount := len(base.Fields())
	c.Core = zapcore.RegisterHooksWithFields(base, func(entry zapcore.Entry, fields []zapcore.Field) error {
		// The fork supplies call-site fields first and a raw bound-context suffix.
		// Use only the former; the latter has already been evaluated into this branch's snapshot.
		if len(fields) < boundCount {
			if runtime := activeOTLP.Load(); runtime != nil {
				runtime.rejected.Add(1)
				return errors.New("invalid application OTLP field context")
			}
			return nil
		}
		return otlpHookWithContext(entry, fields[:len(fields)-boundCount], bound)
	})
	return c
}

// With captures additional context once per output branch, preserving namespace and field order.
func (c *otlpContextCore) With(fields []zapcore.Field) zapcore.Core {
	base := c.base.With(fields)
	bound := c.bound.clone()
	bound.addFields(fields)
	return newOTLPContextCore(base, bound)
}

// Level retains the wrapped Core's configured level without adding a second policy.
func (c *otlpContextCore) Level() zapcore.Level { return zapcore.LevelOf(c.base) }
