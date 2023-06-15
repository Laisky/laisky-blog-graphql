package telegram

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Laisky/laisky-blog-graphql/internal/global"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"
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
func NewMutationResolver(svc service.Interface) MutationResolver {
	return MutationResolver{
		svc: svc,
	}
}

// Telegram telegram resolver
type Telegram struct {
	TelegramAlertTypeResolver *AlertTypeResolver
	TelegramUserResolver      *UserResolver
}

func NewTelegram(ctx context.Context, svc service.Interface) *Telegram {
	setupTelegramThrottle(ctx)
	return &Telegram{
		TelegramAlertTypeResolver: &AlertTypeResolver{svc},
		TelegramUserResolver:      &UserResolver{svc},
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
	page *global.Pagination,
	name string) ([]*model.Users, error) {
	cfg := &dto.QueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return r.svc.LoadUsers(ctx, cfg)
}
func (r *QueryResolver) TelegramAlertTypes(ctx context.Context,
	page *global.Pagination,
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
func (t *UserResolver) ID(ctx context.Context, obj *model.Users) (string, error) {
	return obj.ID.Hex(), nil
}
func (t *UserResolver) CreatedAt(ctx context.Context,
	obj *model.Users,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (t *UserResolver) ModifiedAt(ctx context.Context,
	obj *model.Users,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (t *UserResolver) TelegramID(ctx context.Context,
	obj *model.Users,
) (string, error) {
	return strconv.FormatInt(int64(obj.UID), 10), nil
}
func (t *UserResolver) SubAlerts(ctx context.Context,
	obj *model.Users,
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
) ([]*model.Users, error) {
	return t.svc.LoadUsersByAlertType(ctx, obj)
}

// ============================
// mutations
// ============================

func (r *MutationResolver) TelegramMonitorAlert(ctx context.Context,
	typeArg string,
	token string,
	msg string) (*model.AlertTypes, error) {
	if !telegramThrottle.Allow(typeArg) {
		log.Logger.Warn("deny by throttle", zap.String("type", typeArg))
		return nil, fmt.Errorf("deny by throttle")
	}

	maxlen := gconfig.Shared.GetInt("settings.telegram.max_len")
	if len(msg) > maxlen {
		msg = msg[:maxlen] + " ..."
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
		err = fmt.Errorf(errMsg)
	}

	return alert, err
}
