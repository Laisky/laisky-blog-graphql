// Package controller contains all the controllers used in the application.
package controller

import (
	"context"
	"strings"
	"time"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/general/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/general/service"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	"github.com/Laisky/errors/v2"
	ginMw "github.com/Laisky/gin-middlewares/v6"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"
)

type LocksResolver struct{}

type QueryResolver struct{}
type MutationResolver struct{}

type Type struct {
	LocksResolver *LocksResolver
}

func New() *Type {
	return &Type{
		LocksResolver: new(LocksResolver),
	}
}

var Instance *Type

func Initialize(ctx context.Context) {
	service.Initialize(ctx)

	Instance = New()
}

const (
	generalTokenName       = "general"
	maxTokenExpireDuration = 3600 * 24 * 7 * time.Second // 7d
)

// =================
// query resolver
// =================

func (r *QueryResolver) Lock(ctx context.Context, name string) (*model.Lock, error) {
	return service.Instance.LoadLockByName(ctx, name)
}

func (r *QueryResolver) LockPermissions(ctx context.Context, username string) (users []*models.GeneralUser, err error) {
	log.Logger.Debug("LockPermissions", zap.String("username", username))
	users = []*models.GeneralUser{}
	var (
		prefixes []string
	)
	if username != "" {
		if prefixes = gconfig.Shared.GetStringSlice(
			"settings.general.locks.user_prefix_map." + username); prefixes != nil {
			users = append(users, &models.GeneralUser{
				LockPrefixes: prefixes,
			})
			return users, nil
		}
		return nil, errors.Errorf("user `%v` not exists", username)
	}

	for username = range gconfig.Shared.GetStringMap(
		"settings.general.locks.user_prefix_map") {
		users = append(users, &models.GeneralUser{
			Name: username,
			LockPrefixes: gconfig.Shared.GetStringSlice(
				"settings.general.locks.user_prefix_map." + username),
		})
	}
	return users, nil
}

// --------------------------
// gcp general resolver
// --------------------------
func (r *LocksResolver) ExpiresAt(ctx context.Context,
	obj *model.Lock) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ExpiresAt), nil
}

// ============================
// mutations
// ============================

func validateLockName(ownerName, lockName string) (ok bool) {
	for _, prefix := range gconfig.Shared.GetStringSlice(
		"settings.general.locks.user_prefix_map." + ownerName) {
		if strings.HasPrefix(lockName, prefix) {
			return true
		}
	}

	return false
}

/*
token (`general` in cookie):
::

	{
		"uid": "laisky",
		"exp": 4701974400
	}
*/
func validateAndGetGCPUser(ctx context.Context) (userName string, err error) {
	var token string
	gctx, ok := ginMw.GetGinCtxFromStdCtx(ctx)
	if !ok {
		return "", errors.New("cannot get gin context from standard context")
	}

	if token, err = gctx.
		Cookie(generalTokenName); err != nil {
		return "", errors.Wrap(err, "get jwt token from ctx")
	}

	uc := &jwt.UserClaims{}
	if err = jwt.Instance.ParseClaims(token, uc); err != nil {
		return "", errors.Wrap(err, "parse jwt token")
	}

	return uc.Subject, nil
}

// AcquireLock acquire mutex lock with name and duration.
// if `isRenewal=true`, will renewal exists lock.
func (r *MutationResolver) AcquireLock(ctx context.Context,
	lockName string,
	durationSec int,
	isRenewal *bool,
) (ok bool, err error) {
	if durationSec > gconfig.Shared.GetInt("settings.general.locks.max_duration_sec") {
		return ok, errors.Errorf("duration sec should less than %v",
			gconfig.Shared.GetInt("settings.general.locks.max_duration_sec"))
	}

	var username string
	if username, err = validateAndGetGCPUser(ctx); err != nil {
		log.Logger.Debug("user invalidate", zap.Error(err))
		return ok, err
	}

	if !validateLockName(username, lockName) {
		log.Logger.Warn("user want to acquire lock out of permission",
			zap.String("user", username),
			zap.String("lock", lockName))
		return ok, errors.Errorf("`%v` do not have permission to acquire `%v`",
			username, lockName)
	}

	return service.Instance.AcquireLock(ctx,
		lockName,
		username,
		time.Duration(durationSec)*time.Second,
		false)
}

// CreateGeneralToken generate genaral token than should be set as cookie `general`
func (r *MutationResolver) CreateGeneralToken(ctx context.Context,
	username string,
	durationSec int,
) (token string, err error) {
	log.Logger.Debug("CreateGeneralToken",
		zap.String("username", username),
		zap.Int("durationSec", durationSec))
	if time.Duration(durationSec)*time.Second > maxTokenExpireDuration {
		return "", errors.Errorf(
			"duration should less than %d, got %d",
			maxTokenExpireDuration,
			durationSec)
	}

	// FIXME
	return "", errors.Errorf("not implemented")

	// if _, err = blogSvc.Instance.ValidateAndGetUser(ctx); err != nil {
	// 	return "", errors.Wrapf(err, "user `%v` invalidate", username)
	// }

	// uc := &jwt.UserClaims{
	// 	RegisteredClaims: jwtLib.RegisteredClaims{
	// 		Subject: username,
	// 		ExpiresAt: &jwtLib.NumericDate{
	// 			Time: gutils.Clock.GetUTCNow().Add(time.Duration(durationSec)),
	// 		},
	// 	},
	// }
	// if token, err = jwt.Instance.Sign(uc); err != nil {
	// 	return "", errors.Wrap(err, "generate token")
	// }

	// return token, nil
}
