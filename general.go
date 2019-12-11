package laisky_blog_graphql

import (
	"context"
	"fmt"
	"strings"
	"time"

	middlewares "github.com/Laisky/go-utils/gin-middlewares"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/gcp"
	"github.com/Laisky/laisky-blog-graphql/types"
	"github.com/Laisky/zap"
	jwt "github.com/dgrijalva/jwt-go"
	"github.com/pkg/errors"
)

const (
	generalTokenName = "general"
)

func (r *Resolver) Lock() LockResolver {
	return &locksResolver{r}
}

// ===========================
// query
// ===========================

type locksResolver struct{ *Resolver }

// =================
// query resolver
// =================

func (q *queryResolver) Lock(ctx context.Context, name string) (*gcp.Lock, error) {
	return gcpGeneralDB.LoadLockByName(ctx, name)
}

// --------------------------
// gcp general resolver
// --------------------------
func (r *locksResolver) ExpiresAt(ctx context.Context, obj *gcp.Lock) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.ExpiresAt), nil
}

// ============================
// mutations
// ============================

func validateLockName(ownerName, lockName string) (ok bool) {
	for _, prefix := range utils.Settings.GetStringSlice("settings.gcp.locks.user_prefix_map." + ownerName + ".prefixes") {
		if strings.HasPrefix(lockName, prefix) {
			return true
		}
	}

	return false
}

/*
token:
::
	{
		"uid": "laisky",
		"exp": 4701974400
	}
*/
func validateAndGetGCPUser(ctx context.Context) (userName string, err error) {
	var (
		token   string
		payload jwt.MapClaims
		ok      bool
	)
	if token, err = middlewares.GetGinCtxFromStdCtx(ctx).Cookie(generalTokenName); err != nil {
		return "", errors.Wrap(err, "get jwt token from ctx")
	}

	if payload, err = jwtLib.Validate(token); err != nil {
		return "", errors.Wrap(err, "validate jwt token")
	}

	if userName, ok = payload[jwtLib.GetUserIDKey()].(string); !ok {
		return "", fmt.Errorf("type of " + jwtLib.GetUserIDKey() + " should be string")
	}

	return userName, nil
}

func (r *mutationResolver) AcquireLock(ctx context.Context, lockName string, durationSec int, isRenewal *bool) (ok bool, err error) {
	if durationSec > utils.Settings.GetInt("settings.gcp.locks.max_duration_sec") {
		return ok, fmt.Errorf("duration sec should less than %v", utils.Settings.GetInt("settings.gcp.locks.max_duration_sec"))
	}

	var username string
	if username, err = validateAndGetGCPUser(ctx); err != nil {
		utils.Logger.Debug("user invalidate", zap.Error(err))
		return ok, err
	}
	if !validateLockName(username, lockName) {
		return ok, fmt.Errorf("do not have permission to acquire this lock")
	}

	return gcpGeneralDB.AcquireLock(ctx, lockName, username, time.Duration(durationSec)*time.Second, false)
}
