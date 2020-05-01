package laisky_blog_graphql

import (
	"context"
	"fmt"
	"strconv"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/log"
	"github.com/Laisky/laisky-blog-graphql/telegram"
	"github.com/Laisky/laisky-blog-graphql/types"
	"github.com/Laisky/zap"
)

func (r *Resolver) TelegramAlertType() TelegramAlertTypeResolver {
	return &telegramAlertTypeResolver{r}
}
func (r *Resolver) TelegramUser() TelegramUserResolver {
	return &telegramUserResolver{r}
}

type telegramAlertTypeResolver struct{ *Resolver }
type telegramUserResolver struct{ *Resolver }

// =================
// query resolver
// =================

func (q *queryResolver) TelegramMonitorUsers(ctx context.Context, page *Pagination, name string) ([]*telegram.Users, error) {
	cfg := &telegram.TelegramQueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return monitorDB.LoadUsers(cfg)
}
func (q *queryResolver) TelegramAlertTypes(ctx context.Context, page *Pagination, name string) ([]*telegram.AlertTypes, error) {
	cfg := &telegram.TelegramQueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return monitorDB.LoadAlertTypes(cfg)
}

// --------------------------
// telegram monitor resolver
// --------------------------
func (t *telegramUserResolver) ID(ctx context.Context, obj *telegram.Users) (string, error) {
	return obj.ID.Hex(), nil
}
func (t *telegramUserResolver) CreatedAt(ctx context.Context, obj *telegram.Users) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (t *telegramUserResolver) ModifiedAt(ctx context.Context, obj *telegram.Users) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (t *telegramUserResolver) TelegramID(ctx context.Context, obj *telegram.Users) (string, error) {
	return strconv.FormatInt(int64(obj.UID), 10), nil
}
func (t *telegramUserResolver) SubAlerts(ctx context.Context, obj *telegram.Users) ([]*telegram.AlertTypes, error) {
	return monitorDB.LoadAlertTypesByUser(obj)
}

func (t *telegramAlertTypeResolver) ID(ctx context.Context, obj *telegram.AlertTypes) (string, error) {
	return obj.ID.Hex(), nil
}
func (t *telegramAlertTypeResolver) CreatedAt(ctx context.Context, obj *telegram.AlertTypes) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (t *telegramAlertTypeResolver) ModifiedAt(ctx context.Context, obj *telegram.AlertTypes) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (t *telegramAlertTypeResolver) SubUsers(ctx context.Context, obj *telegram.AlertTypes) ([]*telegram.Users, error) {
	return monitorDB.LoadUsersByAlertType(obj)
}

// ============================
// mutations
// ============================

func (r *mutationResolver) TelegramMonitorAlert(ctx context.Context, typeArg string, token string, msg string) (*telegram.AlertTypes, error) {
	if !telegramThrottle.Allow(typeArg) {
		log.GetLog().Warn("deny by throttle", zap.String("type", typeArg))
		return nil, fmt.Errorf("deny by throttle")
	}

	maxlen := utils.Settings.GetInt("settings.telegram.max_len")
	if len(msg) > maxlen {
		msg = msg[:maxlen] + " ..."
	}

	alert, err := monitorDB.ValidateTokenForAlertType(token, typeArg)
	if err != nil {
		return nil, err
	}
	users, err := monitorDB.LoadUsersByAlertType(alert)
	if err != nil {
		return nil, err
	}

	errMsg := ""
	msg = typeArg + " >>>>>>>>>>>>>>>>>> " + "\n" + msg
	for _, user := range users {
		if err = telegramCli.SendMsgToUser(user.UID, msg); err != nil {
			log.GetLog().Error("send msg to user", zap.Error(err), zap.Int("uid", user.UID), zap.String("msg", msg))
			errMsg += err.Error()
		}
	}

	if errMsg != "" {
		err = fmt.Errorf(errMsg)
	}

	return alert, err
}
