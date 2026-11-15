package userrequests

import (
	"context"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
)

// Bucket lifecycle modes (settings.mcp.tools.user_requests.images.lifecycle_mode).
const (
	// LifecycleModeManage installs the expiry rule of the image prefix and
	// keeps it in place: every check reads the bucket configuration and
	// writes it only when the rule is missing. It is the default.
	LifecycleModeManage = "manage"
	// LifecycleModeVerify never writes the bucket configuration; every check
	// only verifies that some rule (for example one managed by IaC) expires
	// the image prefix within the object TTL.
	LifecycleModeVerify = "verify"
)

const (
	// DefaultImageLifecycleCheckInterval is how often the lifecycle rule is
	// checked after the first re-check. Default: 1h.
	DefaultImageLifecycleCheckInterval = time.Hour
	// imageLifecycleCheckTimeout bounds one check; a write includes the
	// storage settle delay and its bounded retries.
	imageLifecycleCheckTimeout = 2 * time.Minute
	// imageLifecycleMissesBeforeError is how many consecutive manage-mode
	// checks may find the rule gone right after writing it (another writer
	// keeps replacing the configuration) before that is reported at ERROR.
	imageLifecycleMissesBeforeError = 3
)

// imageLifecycleFirstRecheck is when the first check after the start runs:
// soon enough to restore a rule that another deployment's concurrent
// installation erased. A variable only so tests can shrink it.
var imageLifecycleFirstRecheck = time.Minute

// lifecycleState is what the last lifecycle check concluded.
type lifecycleState int

const (
	lifecycleUnknown lifecycleState = iota
	lifecyclePresent
	lifecycleMissing
)

// imageLifecycleController keeps the bucket lifecycle rule of the image
// prefix observable and, in manage mode, in place (maintainer review of PR
// #94). S3 and MinIO offer no conditional write for the lifecycle
// subresource, so a rule this process installed can be erased later by any
// other writer of the bucket; checking again periodically is what bounds how
// long it stays missing.
type imageLifecycleController struct {
	store    storage.ObjectStore
	settings ImageSettings
	logger   logSDK.Logger
	state    lifecycleState
	misses   int
}

// StartLifecycleController checks the bucket lifecycle rule of the image
// prefix in the background: at once, again after imageLifecycleFirstRecheck
// and then every LifecycleCheckInterval (non-positive: only at start) until
// ctx ends. Each check updates mcp_user_image_lifecycle_rule_present. A rule
// found missing, or a check that fails, is reported at ERROR when it starts
// (manage mode: when the rule keeps disappearing right after being written);
// "no automatic cleanup" is reported at ERROR whenever the rule is missing
// while the reconciler is disabled. It returns immediately.
func (m *ImageManager) StartLifecycleController(ctx context.Context, logger logSDK.Logger) {
	if m == nil || m.store == nil {
		return
	}
	c := &imageLifecycleController{store: m.store, settings: m.settings, logger: logger}
	imageMetrics.reconcilerEnabled.Set(boolGauge(c.reconcilerEnabled()))
	if m.settings.ObjectTTLDays <= 0 {
		imageMetrics.lifecycleRulePresent.Set(0)
		if !c.reconcilerEnabled() {
			logger.Error("image objects have no automatic cleanup: object_ttl_days disables the bucket lifecycle rule and the reconciler is disabled",
				zap.String("prefix", m.settings.Prefix))
		}
		return
	}
	go c.run(ctx)
}

// reconcilerEnabled reports whether the orphan-object reconciler runs.
func (c *imageLifecycleController) reconcilerEnabled() bool {
	return c.settings.OrphanSweepInterval > 0
}

// run performs the checks until ctx ends.
func (c *imageLifecycleController) run(ctx context.Context) {
	c.check(ctx)
	wait := imageLifecycleFirstRecheck
	for {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		c.check(ctx)
		if c.settings.LifecycleCheckInterval <= 0 {
			return
		}
		wait = c.settings.LifecycleCheckInterval
	}
}

// check runs one lifecycle check and reports its outcome.
func (c *imageLifecycleController) check(ctx context.Context) bool {
	prefix, days := c.settings.Prefix+"/", c.settings.ObjectTTLDays
	checkCtx, cancel := context.WithTimeout(ctx, imageLifecycleCheckTimeout)
	var err error
	if c.settings.LifecycleMode == LifecycleModeVerify {
		err = c.store.CheckLifecycle(checkCtx, prefix, days)
	} else {
		err = c.store.EnsureLifecycle(checkCtx, prefix, days)
	}
	cancel()
	if ctx.Err() != nil {
		return false // shutting down: the outcome says nothing about the bucket
	}
	present := err == nil
	imageMetrics.lifecycleRulePresent.Set(boolGauge(present))
	c.report(err)
	return present
}

// report logs the outcome of a check, once per change of state.
func (c *imageLifecycleController) report(err error) {
	fields := make([]zap.Field, 0, 6)
	fields = append(fields,
		zap.String("bucket", c.settings.Bucket),
		zap.String("prefix", c.settings.Prefix),
		zap.Int("ttl_days", c.settings.ObjectTTLDays),
		zap.String("lifecycle_mode", c.lifecycleMode()),
		zap.Bool("orphan_reconciler_enabled", c.reconcilerEnabled()),
	)
	if err == nil {
		if c.state == lifecycleMissing {
			c.logger.Info("image bucket lifecycle rule is in place again", fields...)
		}
		c.state, c.misses = lifecyclePresent, 0
		return
	}
	fields = append(fields, zap.Error(err))
	var msg string
	switch {
	case errors.Is(err, storage.ErrLifecycleRuleMissing) && c.settings.LifecycleMode == LifecycleModeVerify:
		msg = "image bucket lifecycle rule missing; lifecycle_mode is verify, so add a rule that expires the prefix within object_ttl_days (for example with IaC)"
	case errors.Is(err, storage.ErrLifecycleRuleMissing):
		c.misses++
		if c.misses < imageLifecycleMissesBeforeError {
			c.logger.Warn("image bucket lifecycle rule was replaced by another writer; it is reinstalled at the next check", fields...)
			return
		}
		msg = "image bucket lifecycle rule keeps disappearing: another writer replaces the bucket lifecycle; let one writer own it, or set lifecycle_mode to verify and manage the rule there"
	default:
		msg = "install image bucket lifecycle rule failed; add it by hand, until then only the reconciler removes objects of expired refs"
		if c.settings.LifecycleMode == LifecycleModeVerify {
			msg = "check image bucket lifecycle rule failed; until it can be read its presence is unknown"
		}
	}
	if c.state == lifecycleMissing {
		c.logger.Debug(msg, fields...)
		return
	}
	c.state = lifecycleMissing
	c.logger.Error(msg, fields...)
	if !c.reconcilerEnabled() {
		c.logger.Error("image objects have no automatic cleanup: the bucket lifecycle rule is missing and the reconciler is disabled", fields...)
	}
}

// lifecycleMode returns the effective lifecycle mode.
func (c *imageLifecycleController) lifecycleMode() string {
	if c.settings.LifecycleMode == LifecycleModeVerify {
		return LifecycleModeVerify
	}
	return LifecycleModeManage
}
