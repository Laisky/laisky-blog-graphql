package general

import (
	"context"
	"fmt"
	"time"

	"laisky-blog-graphql/internal/web/general/db"
	"laisky-blog-graphql/library/log"

	"cloud.google.com/go/firestore"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

type Service struct {
	db *db.DB
}

func NewService(db *db.DB) *Service {
	return &Service{db: db}
}

func (s *Service) AcquireLock(ctx context.Context, name, ownerID string, duration time.Duration, isRenewal bool) (ok bool, err error) {
	log.Logger.Info("AcquireLock", zap.String("name", name), zap.String("owner", ownerID), zap.Duration("duration", duration))
	ref := s.db.GetLocksCol().Doc(name)
	now := utils.Clock.GetUTCNow()
	err = s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		doc, err := tx.Get(ref)
		if err != nil && doc == nil {
			log.Logger.Warn("load gcp general lock", zap.String("name", name), zap.Error(err))
			return errors.Wrap(err, "load lock docu")
		}
		if !doc.Exists() && isRenewal {
			return fmt.Errorf("lock `%v` not exists", name)
		}

		lock := &Lock{}
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

func (s *Service) LoadLockByName(ctx context.Context, name string) (lock *Lock, err error) {
	log.Logger.Debug("load lock by name", zap.String("name", name))
	docu, err := s.db.GetLocksCol().Doc(name).Get(ctx)
	if err != nil && docu == nil {
		log.Logger.Error("load gcp general lock", zap.String("name", name), zap.Error(err))
		return nil, errors.Wrap(err, "load docu by name")
	}

	lock = &Lock{}
	if err = docu.DataTo(lock); err != nil {
		return nil, errors.Wrap(err, "load data to type Lock")
	}
	return lock, nil
}
