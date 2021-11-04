package telegram

import (
	"context"
	"fmt"
	"strconv"

	"laisky-blog-graphql/internal/global"
	"laisky-blog-graphql/internal/web/telegram/dto"
	"laisky-blog-graphql/internal/web/telegram/model"
	"laisky-blog-graphql/internal/web/telegram/service"
	"laisky-blog-graphql/library"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type TelegramAlertTypeResolver struct{}
type TelegramUserResolver struct{}

type QueryResolver struct{}
type MutationResolver struct{}

type Type struct {
	TelegramAlertTypeResolver *TelegramAlertTypeResolver
	TelegramUserResolver      *TelegramUserResolver
}

var Instance *Type

func Initialize(ctx context.Context) {
	service.Initialize(ctx)

	setupTelegramThrottle(ctx)

	Instance = &Type{
		TelegramAlertTypeResolver: new(TelegramAlertTypeResolver),
		TelegramUserResolver:      new(TelegramUserResolver),
	}
}

func (r *QueryResolver) TelegramMonitorUsers(ctx context.Context,
	page *global.Pagination,
	name string) ([]*model.Users, error) {
	cfg := &dto.QueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return service.Instance.LoadUsers(cfg)
}
func (r *QueryResolver) TelegramAlertTypes(ctx context.Context,
	page *global.Pagination,
	name string) ([]*model.AlertTypes, error) {
	cfg := &dto.QueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return service.Instance.LoadAlertTypes(cfg)
}

// --------------------------
// telegram monitor resolver
// --------------------------
func (t *TelegramUserResolver) ID(ctx context.Context, obj *model.Users) (string, error) {
	return obj.ID.Hex(), nil
}
func (t *TelegramUserResolver) CreatedAt(ctx context.Context,
	obj *model.Users,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (t *TelegramUserResolver) ModifiedAt(ctx context.Context,
	obj *model.Users,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (t *TelegramUserResolver) TelegramID(ctx context.Context,
	obj *model.Users,
) (string, error) {
	return strconv.FormatInt(int64(obj.UID), 10), nil
}
func (t *TelegramUserResolver) SubAlerts(ctx context.Context,
	obj *model.Users,
) ([]*model.AlertTypes, error) {
	return service.Instance.LoadAlertTypesByUser(obj)
}

func (t *TelegramAlertTypeResolver) ID(ctx context.Context,
	obj *model.AlertTypes,
) (string, error) {
	return obj.ID.Hex(), nil
}
func (t *TelegramAlertTypeResolver) CreatedAt(ctx context.Context,
	obj *model.AlertTypes,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (t *TelegramAlertTypeResolver) ModifiedAt(ctx context.Context,
	obj *model.AlertTypes,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (t *TelegramAlertTypeResolver) SubUsers(ctx context.Context,
	obj *model.AlertTypes,
) ([]*model.Users, error) {
	return service.Instance.LoadUsersByAlertType(obj)
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

	maxlen := gutils.Settings.GetInt("settings.telegram.max_len")
	if len(msg) > maxlen {
		msg = msg[:maxlen] + " ..."
	}

	alert, err := service.Instance.ValidateTokenForAlertType(token, typeArg)
	if err != nil {
		return nil, err
	}

	users, err := service.Instance.LoadUsersByAlertType(alert)
	if err != nil {
		return nil, err
	}

	errMsg := ""
	msg = typeArg + " >>>>>>>>>>>>>>>>>> " + "\n" + msg
	for _, user := range users {
		if err = service.Instance.SendMsgToUser(user.UID, msg); err != nil {
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
