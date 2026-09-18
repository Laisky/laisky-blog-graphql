package telegram

import (
	"context"
	"strconv"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/formatting"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	"github.com/Laisky/laisky-blog-graphql/library"
)

// errTelegramServiceUnavailable is the base sentinel returned by resolver
// methods when the telegram service failed to initialize (e.g. tb.NewBot
// rejected the token or required databases were unreachable). Without this
// guard the resolver dereferences a nil service interface and panics for
// every request. Callers should wrap it with errors.WithStack so the stack
// reflects the actual return site rather than this var declaration.
var errTelegramServiceUnavailable = errors.New("telegram service not initialized")

// errTelegramThrottleUnavailable is returned when the alert rate limiter could
// not be built from configuration. Alerting is refused rather than sent
// unthrottled, and startup is not aborted for an optional subsystem.
var errTelegramThrottleUnavailable = errors.New("telegram alert throttle is not configured")

// AlertTypeResolver alert type resolver
type AlertTypeResolver struct {
	svc service.Interface
}

// UserResolver user resolver
type UserResolver struct {
	svc service.Interface
}

// QueryResolver query resolver
type QueryResolver struct {
	svc service.Interface
}

// MutationResolver mutation resolver
type MutationResolver struct {
	svc service.Interface
}

// NewQueryResolver new query resolver
func NewQueryResolver(svc service.Interface) QueryResolver {
	return QueryResolver{
		svc: svc,
	}
}

// NewMutationResolver new mutation resolver
func NewMutationResolver(svc service.Interface) *MutationResolver {
	return &MutationResolver{
		svc: svc,
	}
}

// Telegram telegram resolver
type Telegram struct {
	TelegramAlertTypeResolver   *AlertTypeResolver
	TelegramMonitorUserResolver *UserResolver
}

// NewTelegram builds the telegram GraphQL resolvers and installs the alert
// rate limiter for this process. A nil service is allowed: each resolver method
// then returns errTelegramServiceUnavailable instead of dereferencing it.
func NewTelegram(ctx context.Context, svc service.Interface) *Telegram {
	setupTelegramThrottle(ctx)
	return &Telegram{
		TelegramAlertTypeResolver:   &AlertTypeResolver{svc},
		TelegramMonitorUserResolver: &UserResolver{svc},
	}
}

// func isEnable() bool {
// 	return gconfig.Shared.Get("settings.telegram") != nil
// }

// func Initialize(ctx context.Context) {
// 	if !isEnable() {
// 		return
// 	}

// 	service.Initialize(ctx)

// 	setupTelegramThrottle(ctx)

// 	Instance = &Type{
// 		TelegramAlertTypeResolver: new(AlertTypeResolver),
// 		TelegramUserResolver:      new(UserResolver),
// 	}
// }

// TelegramMonitorUsers resolves the paged monitored-user list.
func (r *QueryResolver) TelegramMonitorUsers(ctx context.Context,
	page *models.Pagination,
	name string) ([]*model.MonitorUsers, error) {
	if r == nil || r.svc == nil {
		return nil, errors.WithStack(errTelegramServiceUnavailable)
	}
	cfg := &dto.QueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return r.svc.LoadUsers(ctx, cfg)
}

// TelegramAlertTypes resolves the paged alert-type list.
func (r *QueryResolver) TelegramAlertTypes(ctx context.Context,
	page *models.Pagination,
	name string) ([]*model.AlertTypes, error) {
	if r == nil || r.svc == nil {
		return nil, errors.WithStack(errTelegramServiceUnavailable)
	}
	cfg := &dto.QueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return r.svc.LoadAlertTypes(ctx, cfg)
}

// --------------------------
// telegram monitor resolver
// --------------------------
func (t *UserResolver) ID(ctx context.Context, obj *model.MonitorUsers) (string, error) {
	return obj.ID.Hex(), nil
}

// CreatedAt resolves the monitored user's creation timestamp.
func (t *UserResolver) CreatedAt(ctx context.Context,
	obj *model.MonitorUsers,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}

// ModifiedAt resolves the monitored user's last-modified timestamp.
func (t *UserResolver) ModifiedAt(ctx context.Context,
	obj *model.MonitorUsers,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}

// TelegramID resolves the monitored user's Telegram numeric ID as a string,
// because the value exceeds the range of a GraphQL Int.
func (t *UserResolver) TelegramID(ctx context.Context,
	obj *model.MonitorUsers,
) (string, error) {
	return strconv.FormatInt(int64(obj.UID), 10), nil
}

// SubAlerts resolves the alert types this monitored user subscribes to.
func (t *UserResolver) SubAlerts(ctx context.Context,
	obj *model.MonitorUsers,
) ([]*model.AlertTypes, error) {
	if t == nil || t.svc == nil {
		return nil, errors.WithStack(errTelegramServiceUnavailable)
	}
	return t.svc.LoadAlertTypesByUser(ctx, obj)
}

// ID resolves the alert type's hex object identifier.
func (t *AlertTypeResolver) ID(ctx context.Context,
	obj *model.AlertTypes,
) (string, error) {
	return obj.ID.Hex(), nil
}

// CreatedAt resolves the alert type's creation timestamp.
func (t *AlertTypeResolver) CreatedAt(ctx context.Context,
	obj *model.AlertTypes,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}

// ModifiedAt resolves the alert type's last-modified timestamp.
func (t *AlertTypeResolver) ModifiedAt(ctx context.Context,
	obj *model.AlertTypes,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}

// SubUsers resolves the monitored users subscribed to this alert type.
func (t *AlertTypeResolver) SubUsers(ctx context.Context,
	obj *model.AlertTypes,
) ([]*model.MonitorUsers, error) {
	if t == nil || t.svc == nil {
		return nil, errors.WithStack(errTelegramServiceUnavailable)
	}
	return t.svc.LoadUsersByAlertType(ctx, obj)
}

// ============================
// mutations
// ============================

// TelegramMonitorAlert pushes one throttled alert message. It refuses the send
// when the alert rate limiter is not configured, rather than bypassing it.
func (r *MutationResolver) TelegramMonitorAlert(ctx context.Context,
	typeArg string,
	token string,
	msg string) (*model.AlertTypes, error) {
	if r == nil || r.svc == nil {
		return nil, errors.WithStack(errTelegramServiceUnavailable)
	}
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_alert")
	// The limiter is absent when telegram throttling was never configured or is
	// unusable. Refuse the alert instead of dereferencing nil or, worse, pushing
	// unlimited alerts because the guard was skipped.
	if telegramRatelimiter == nil {
		logger.Error("telegram alert throttle is not configured; refusing to send")
		return nil, errors.WithStack(errTelegramThrottleUnavailable)
	}
	if !telegramRatelimiter.Allow(typeArg) { //nolint:contextcheck // Allow is a rate-limiter check that does not need request context
		// logger.Warn("deny by throttle", zap.String("type", typeArg))
		return nil, errors.Errorf("deny by throttle")
	}

	maxlen := gconfig.Shared.GetInt("settings.telegram.max_len")
	if maxlen <= 0 || maxlen > 3000 {
		logger.Warn("invalid max len, reset to 3000", zap.Int("maxlen", maxlen))
		maxlen = 3000
	}

	// Truncate message if too long.
	truncatedMsg := library.Truncate(msg, maxlen)
	if len(truncatedMsg) < len(msg) {
		msg = escapeMsg(truncatedMsg) + "..."
	} else {
		msg = escapeMsg(msg)
	}

	alert, err := r.svc.ValidateTokenForAlertType(ctx, token, typeArg)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	users, err := r.svc.LoadUsersByAlertType(ctx, alert)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	errMsg := ""
	msg = typeArg + " >>>>>>>>>>>>>>>>>> " + "\n" + msg
	for _, user := range users {
		if err = r.svc.SendMsgToUser(user.UID, msg); err != nil {
			logger.Error("send msg to user",
				zap.Error(err),
				zap.Int("uid", user.UID),
				zap.String("msg", msg))
			errMsg += err.Error()
		}
	}

	if errMsg != "" {
		err = errors.New(errMsg)
	}

	if err != nil {
		return alert, errors.WithStack(err)
	}
	return alert, nil
}

// escapeMsg escapes special characters in a message to prevent Telegram from interpreting them as formatting
func escapeMsg(msg string) string {
	return formatting.EscapeTelegramMarkdown(msg)
}
