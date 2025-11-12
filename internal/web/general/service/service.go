// Package service contains all the business logic.
package service

import (
	"context"
	"time"

	"github.com/Laisky/laisky-blog-graphql/internal/web/general/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/general/model"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	"cloud.google.com/go/firestore"
	"github.com/Laisky/errors/v2"
	"github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
)

var Instance *Type

type Type struct {
	dao     *dao.Type
	tasksDB *rlibs.DB
}

func Initialize(ctx context.Context) {
	dao.Initialize(ctx)

	Instance = NewService(dao.Instance)
}

func NewService(db *dao.Type) *Type {
	return &Type{dao: db}
}

// SetTasksDB sets the Redis client used for background task operations.
func (s *Type) SetTasksDB(db *rlibs.DB) {
	s.tasksDB = db
}

func (s *Type) getTasksDB() (*rlibs.DB, error) {
	if s.tasksDB == nil {
		return nil, errors.New("tasks redis db is not configured")
	}
	return s.tasksDB, nil
}

// AddLLMStormTask enqueues a new LLM storm task using the provided prompt and API key.
func (s *Type) AddLLMStormTask(ctx context.Context, prompt, apiKey string) (string, error) {
	rdb, err := s.getTasksDB()
	if err != nil {
		return "", errors.Wrap(err, "get tasks db")
	}

	taskID, err := rdb.AddLLMStormTask(ctx, prompt, apiKey)
	if err != nil {
		return "", errors.Wrap(err, "add llm storm task")
	}

	return taskID, nil
}

// GetLLMStormTaskResult retrieves the LLM storm task result with the given task ID.
func (s *Type) GetLLMStormTaskResult(ctx context.Context, taskID string) (*rlibs.LLMStormTask, error) {
	rdb, err := s.getTasksDB()
	if err != nil {
		return nil, errors.Wrap(err, "get tasks db")
	}

	task, err := rdb.GetLLMStormTaskResult(ctx, taskID)
	if err != nil {
		return nil, errors.Wrapf(err, "get llm storm task result %q", taskID)
	}

	return task, nil
}

// AddHTMLCrawlerTask enqueues an HTML crawler task for the specified URL.
func (s *Type) AddHTMLCrawlerTask(ctx context.Context, url string) (string, error) {
	rdb, err := s.getTasksDB()
	if err != nil {
		return "", errors.Wrap(err, "get tasks db")
	}

	taskID, err := rdb.AddHTMLCrawlerTask(ctx, url)
	if err != nil {
		return "", errors.Wrap(err, "add html crawler task")
	}

	return taskID, nil
}

// GetHTMLCrawlerTask dequeues the next available HTML crawler task.
func (s *Type) GetHTMLCrawlerTask(ctx context.Context) (*rlibs.HTMLCrawlerTask, error) {
	rdb, err := s.getTasksDB()
	if err != nil {
		return nil, errors.Wrap(err, "get tasks db")
	}

	task, err := rdb.GetHTMLCrawlerTask(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "get html crawler task")
	}

	return task, nil
}

func (s *Type) AcquireLock(ctx context.Context,
	name, ownerID string,
	duration time.Duration,
	isRenewal bool,
) (ok bool, err error) {
	log.Logger.Info("AcquireLock",
		zap.String("name", name),
		zap.String("owner", ownerID),
		zap.Duration("duration", duration))
	ref := s.dao.GetLocksCol().Doc(name)
	now := utils.Clock.GetUTCNow()
	err = s.dao.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		doc, err := tx.Get(ref)
		if err != nil && doc == nil {
			return errors.Wrap(err, "load lock docu")
		}
		if !doc.Exists() && isRenewal {
			return errors.Errorf("lock `%v` not exists", name)
		}

		lock := &model.Lock{}
		// check whether expired
		if doc.Exists() && !isRenewal {
			if err = doc.DataTo(lock); err != nil {
				return errors.Wrap(err, "convert gcp docu to go struct")
			}
			if lock.OwnerID != ownerID && lock.ExpiresAt.After(now) { // still locked
				return nil
			}
		}

		lock.OwnerID = ownerID
		lock.Name = name
		lock.ExpiresAt = now.Add(duration)
		ok = true
		return tx.Set(ref, lock)
	})
	return
}

func (s *Type) LoadLockByName(ctx context.Context,
	name string) (lock *model.Lock, err error) {
	log.Logger.Debug("load lock by name", zap.String("name", name))
	docu, err := s.dao.GetLocksCol().Doc(name).Get(ctx)
	if err != nil && docu == nil {
		return nil, errors.Wrap(err, "load docu by name")
	}

	lock = &model.Lock{}
	if err = docu.DataTo(lock); err != nil {
		return nil, errors.Wrap(err, "load data to type Lock")
	}
	return lock, nil
}
