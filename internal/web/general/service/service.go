package service

import (
	"context"
	"fmt"
	"time"

	"laisky-blog-graphql/internal/web/general/dao"
	"laisky-blog-graphql/internal/web/general/model"
	"laisky-blog-graphql/library/log"

	"cloud.google.com/go/firestore"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

var Instance *Type

type Type struct {
	dao *dao.Type
}

func Initialize(ctx context.Context) {
	dao.Initialize(ctx)

	Instance = NewService(dao.Instance)
}

func NewService(db *dao.Type) *Type {
	return &Type{dao: db}
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
			log.Logger.Warn("load gcp general lock",
				zap.String("name", name),
				zap.Error(err))
			return errors.Wrap(err, "load lock docu")
		}
		if !doc.Exists() && isRenewal {
			return fmt.Errorf("lock `%v` not exists", name)
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
		log.Logger.Error("load gcp general lock",
			zap.String("name", name),
			zap.Error(err))
		return nil, errors.Wrap(err, "load docu by name")
	}

	lock = &model.Lock{}
	if err = docu.DataTo(lock); err != nil {
		return nil, errors.Wrap(err, "load data to type Lock")
	}
	return lock, nil
}
