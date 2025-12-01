// Package controller contains all the controllers used in the application.
package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/general/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/general/service"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
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

// ConfigureTaskStore injects the Redis client used for task queue operations.
func ConfigureTaskStore(db *rlibs.DB) {
	if db == nil {
		log.Logger.Warn("skip configuring general task store with nil redis client")
		return
	}

	if service.Instance == nil {
		log.Logger.Warn("general service not initialized while configuring task store")
		return
	}

	service.Instance.SetTasksDB(db)
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
	logger := gmw.GetLogger(ctx).Named("lock_permissions")
	logger.Debug("LockPermissions", zap.String("username", username))
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

// GeneralGetLLMStormTaskResult resolves the LLM storm task result for authenticated workers.
func (r *QueryResolver) GeneralGetLLMStormTaskResult(ctx context.Context, taskID string) (*models.GeneralLLMStormTask, error) {
	logger := gmw.GetLogger(ctx).
		Named("general_llm_storm_task_result").
		With(zap.String("task_id", taskID))

	if service.Instance == nil {
		return nil, errors.New("general service not initialized")
	}

	uc := &jwt.UserClaims{}
	if err := auth.Instance.GetUserClaims(ctx, uc); err != nil {
		return nil, errors.Wrap(err, "validate worker")
	}
	logger = logger.With(zap.String("username", uc.Subject))

	task, err := service.Instance.GetLLMStormTaskResult(ctx, taskID)
	if err != nil {
		return nil, errors.Wrap(err, "load llm storm task result")
	}

	result, err := newGeneralLLMStormTask(task)
	if err != nil {
		return nil, errors.Wrap(err, "build graphql llm storm task")
	}

	logger.Info("fetched llm storm task result")
	return result, nil
}

// GeneralGetHTMLCrawlerTask dequeues the next HTML crawler task for authenticated workers.
func (r *QueryResolver) GeneralGetHTMLCrawlerTask(ctx context.Context) (*models.GeneralHTMLCrawlerTask, error) {
	logger := gmw.GetLogger(ctx).
		Named("general_html_crawler_task")

	if service.Instance == nil {
		return nil, errors.New("general service not initialized")
	}

	uc := &jwt.UserClaims{}
	if err := auth.Instance.GetUserClaims(ctx, uc); err != nil {
		return nil, errors.Wrap(err, "validate worker")
	}
	logger = logger.With(zap.String("username", uc.Subject))

	task, err := service.Instance.GetHTMLCrawlerTask(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "fetch html crawler task")
	}

	result, err := newGeneralHTMLCrawlerTask(task)
	if err != nil {
		return nil, errors.Wrap(err, "build graphql html crawler task")
	}

	logger.Info("dequeued html crawler task", zap.String("task_id", result.TaskID))
	return result, nil
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

func newGeneralLLMStormTask(task *rlibs.LLMStormTask) (*models.GeneralLLMStormTask, error) {
	if task == nil {
		return nil, errors.New("llm storm task is nil")
	}

	createdAt := library.NewDatetimeFromTime(task.CreatedAt.UTC())
	if createdAt == nil {
		return nil, errors.New("failed to convert created_at")
	}

	result := &models.GeneralLLMStormTask{
		TaskID:        task.TaskID,
		CreatedAt:     *createdAt,
		Status:        task.Status,
		FailedReason:  task.FailedReason,
		Prompt:        task.Prompt,
		APIKey:        task.APIKey,
		ResultArticle: task.ResultArticle,
		Runner:        task.Runner,
	}

	if task.FinishedAt != nil {
		result.FinishedAt = library.NewDatetimeFromTime(task.FinishedAt.UTC())
	}

	if task.ResultReferences != nil {
		data, err := json.Marshal(task.ResultReferences)
		if err != nil {
			return nil, errors.Wrap(err, "marshal result references")
		}
		jsonStr := library.JSONString(string(data))
		result.ResultReferences = &jsonStr
	}

	return result, nil
}

func newGeneralHTMLCrawlerTask(task *rlibs.HTMLCrawlerTask) (*models.GeneralHTMLCrawlerTask, error) {
	if task == nil {
		return nil, errors.New("html crawler task is nil")
	}

	createdAt := library.NewDatetimeFromTime(task.CreatedAt.UTC())
	if createdAt == nil {
		return nil, errors.New("failed to convert created_at")
	}

	result := &models.GeneralHTMLCrawlerTask{
		TaskID:       task.TaskID,
		CreatedAt:    *createdAt,
		Status:       task.Status,
		FailedReason: task.FailedReason,
		URL:          task.Url,
	}

	if task.FinishedAt != nil {
		result.FinishedAt = library.NewDatetimeFromTime(task.FinishedAt.UTC())
	}

	if len(task.ResultHTML) > 0 {
		encoded := base64.StdEncoding.EncodeToString(task.ResultHTML)
		result.ResultHTMLB64 = &encoded
	}

	return result, nil
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
	gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
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
		return ok, err
	}

	if !validateLockName(username, lockName) {
		return ok, errors.Errorf("`%v` do not have permission to acquire `%v`",
			username, lockName)
	}

	return service.Instance.AcquireLock(ctx,
		lockName,
		username,
		time.Duration(durationSec)*time.Second,
		false)
}

// GeneralAddLLMStormTask enqueues an LLM storm task for the authenticated worker.
func (r *MutationResolver) GeneralAddLLMStormTask(ctx context.Context, prompt string, apiKey string) (string, error) {
	logger := gmw.GetLogger(ctx).
		Named("general_add_llm_storm_task")

	if service.Instance == nil {
		return "", errors.New("general service not initialized")
	}

	uc := &jwt.UserClaims{}
	if err := auth.Instance.GetUserClaims(ctx, uc); err != nil {
		return "", errors.Wrap(err, "validate worker")
	}
	logger = logger.With(zap.String("username", uc.Subject))

	taskID, err := service.Instance.AddLLMStormTask(ctx, prompt, apiKey)
	if err != nil {
		return "", errors.Wrap(err, "enqueue llm storm task")
	}

	logger.Info("enqueued llm storm task", zap.String("task_id", taskID))
	return taskID, nil
}

// GeneralAddHTMLCrawlerTask enqueues an HTML crawler task for the authenticated worker.
func (r *MutationResolver) GeneralAddHTMLCrawlerTask(ctx context.Context, url string) (string, error) {
	logger := gmw.GetLogger(ctx).
		Named("general_add_html_crawler_task")

	if service.Instance == nil {
		return "", errors.New("general service not initialized")
	}

	uc := &jwt.UserClaims{}
	if err := auth.Instance.GetUserClaims(ctx, uc); err != nil {
		return "", errors.Wrap(err, "validate worker")
	}
	logger = logger.With(zap.String("username", uc.Subject))

	taskID, err := service.Instance.AddHTMLCrawlerTask(ctx, url)
	if err != nil {
		return "", errors.Wrap(err, "enqueue html crawler task")
	}

	logger.Info("enqueued html crawler task", zap.String("task_id", taskID))
	return taskID, nil
}

// CreateGeneralToken generate genaral token than should be set as cookie `general`
func (r *MutationResolver) CreateGeneralToken(ctx context.Context,
	username string,
	durationSec int,
) (token string, err error) {
	logger := gmw.GetLogger(ctx).Named("create_general_token")
	logger.Debug("CreateGeneralToken",
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
