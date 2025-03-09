package telegram

import (
	"context"
	"strconv"
	"strings"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

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

func (r *QueryResolver) TelegramMonitorUsers(ctx context.Context,
	page *models.Pagination,
	name string) ([]*model.MonitorUsers, error) {
	cfg := &dto.QueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return r.svc.LoadUsers(ctx, cfg)
}
func (r *QueryResolver) TelegramAlertTypes(ctx context.Context,
	page *models.Pagination,
	name string) ([]*model.AlertTypes, error) {
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
func (t *UserResolver) CreatedAt(ctx context.Context,
	obj *model.MonitorUsers,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (t *UserResolver) ModifiedAt(ctx context.Context,
	obj *model.MonitorUsers,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (t *UserResolver) TelegramID(ctx context.Context,
	obj *model.MonitorUsers,
) (string, error) {
	return strconv.FormatInt(int64(obj.UID), 10), nil
}
func (t *UserResolver) SubAlerts(ctx context.Context,
	obj *model.MonitorUsers,
) ([]*model.AlertTypes, error) {
	return t.svc.LoadAlertTypesByUser(ctx, obj)
}

func (t *AlertTypeResolver) ID(ctx context.Context,
	obj *model.AlertTypes,
) (string, error) {
	return obj.ID.Hex(), nil
}
func (t *AlertTypeResolver) CreatedAt(ctx context.Context,
	obj *model.AlertTypes,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (t *AlertTypeResolver) ModifiedAt(ctx context.Context,
	obj *model.AlertTypes,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (t *AlertTypeResolver) SubUsers(ctx context.Context,
	obj *model.AlertTypes,
) ([]*model.MonitorUsers, error) {
	return t.svc.LoadUsersByAlertType(ctx, obj)
}

// ============================
// mutations
// ============================

func (r *MutationResolver) TelegramMonitorAlert(ctx context.Context,
	typeArg string,
	token string,
	msg string) (*model.AlertTypes, error) {
	if !telegramRatelimiter.Allow(typeArg) {
		// log.Logger.Warn("deny by throttle", zap.String("type", typeArg))
		return nil, errors.Errorf("deny by throttle")
	}

	maxlen := gconfig.Shared.GetInt("settings.telegram.max_len")
	if maxlen <= 0 || maxlen > 3000 {
		log.Logger.Warn("invalid max len, reset to 3000", zap.Int("maxlen", maxlen))
		maxlen = 3000
	}

	// Truncate message if too long, preserving closing delimiters if present
	msgRunes := []rune(msg)
	if len(msgRunes) > maxlen {
		msg = escapeMsg(string(msgRunes[:maxlen]) + "...")
	}

	alert, err := r.svc.ValidateTokenForAlertType(ctx, token, typeArg)
	if err != nil {
		return nil, err
	}

	users, err := r.svc.LoadUsersByAlertType(ctx, alert)
	if err != nil {
		return nil, err
	}

	errMsg := ""
	msg = typeArg + " >>>>>>>>>>>>>>>>>> " + "\n" + msg
	for _, user := range users {
		if err = r.svc.SendMsgToUser(user.UID, msg); err != nil {
			log.Logger.Error("send msg to user",
				zap.Error(err),
				zap.Int("uid", user.UID),
				zap.String("msg", msg))
			errMsg += err.Error()
		}
	}

	if errMsg != "" {
		err = errors.New(errMsg)
	}

	return alert, err
}

// escapeMsg escapes special characters in a message to prevent Telegram from interpreting them as formatting
func escapeMsg(msg string) string {
	// Escape special characters that Telegram interprets as formatting
	// Replace backticks with single quotes to avoid code block formatting issues
	msg = strings.ReplaceAll(msg, "`", "'")

	// Escape other special Telegram formatting characters
	msg = strings.ReplaceAll(msg, "_", "\\_")
	msg = strings.ReplaceAll(msg, "*", "\\*")
	msg = strings.ReplaceAll(msg, "[", "\\[")
	msg = strings.ReplaceAll(msg, "]", "\\]")
	msg = strings.ReplaceAll(msg, "(", "\\(")
	msg = strings.ReplaceAll(msg, ")", "\\)")
	msg = strings.ReplaceAll(msg, "~", "\\~")
	msg = strings.ReplaceAll(msg, ">", "\\>")
	msg = strings.ReplaceAll(msg, "#", "\\#")
	msg = strings.ReplaceAll(msg, "+", "\\+")
	msg = strings.ReplaceAll(msg, "-", "\\-")
	msg = strings.ReplaceAll(msg, "=", "\\=")
	msg = strings.ReplaceAll(msg, "|", "\\|")
	msg = strings.ReplaceAll(msg, "{", "\\{")
	msg = strings.ReplaceAll(msg, "}", "\\}")

	return msg
}
