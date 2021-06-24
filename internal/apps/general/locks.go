package general

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"

	"laisky-blog-graphql/internal/models"
	"laisky-blog-graphql/library/log"
)

const (
	locksColName = "Locks"
)

type DB struct {
	dbcli *models.Firestore
}

type Lock struct {
	Name      string    `firestore:"name"`
	OwnerID   string    `firestore:"owner_id"`
	ExpiresAt time.Time `firestore:"expires_at"`
}

func NewGeneralDB(db *models.Firestore) *DB {
	return &DB{
		dbcli: db,
	}
}

func (db *DB) GetLocksCol() *firestore.CollectionRef {
	return db.dbcli.Collection(locksColName)
}

func (db *DB) AcquireLock(ctx context.Context, name, ownerID string, duration time.Duration, isRenewal bool) (ok bool, err error) {
	log.Logger.Info("AcquireLock", zap.String("name", name), zap.String("owner", ownerID), zap.Duration("duration", duration))
	ref := db.GetLocksCol().Doc(name)
	now := utils.Clock.GetUTCNow()
	err = db.dbcli.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
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

func (db *DB) LoadLockByName(ctx context.Context, name string) (lock *Lock, err error) {
	log.Logger.Debug("load lock by name", zap.String("name", name))
	docu, err := db.GetLocksCol().Doc(name).Get(ctx)
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
