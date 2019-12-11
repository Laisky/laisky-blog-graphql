package gcp

import (
	"context"
	"fmt"
	"time"

	"github.com/Laisky/zap"

	"cloud.google.com/go/firestore"
	"github.com/Laisky/go-utils"
	"github.com/pkg/errors"

	"github.com/Laisky/laisky-blog-graphql/models"
)

const (
	locksColName = "Locks"
)

type GeneralDB struct {
	dbcli *models.Firestore
}

type Lock struct {
	Name      string    `firestore:"name"`
	OwnerID   string    `firestore:"owner_id"`
	ExpiresAt time.Time `firestore:"expires_at"`
}

func NewGeneralDB(db *models.Firestore) *GeneralDB {
	return &GeneralDB{
		dbcli: db,
	}
}

func (db *GeneralDB) GetLocksCol() *firestore.CollectionRef {
	return db.dbcli.Collection(locksColName)
}

func (db *GeneralDB) AcquireLock(ctx context.Context, name, ownerID string, duration time.Duration, isRenewal bool) (ok bool, err error) {
	utils.Logger.Info("AcquireLock", zap.String("name", name), zap.String("owner", ownerID), zap.Duration("duration", duration))
	ref := db.GetLocksCol().Doc(name)
	now := utils.Clock.GetUTCNow()
	err = db.dbcli.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		doc, err := tx.Get(ref)
		if err != nil && doc == nil {
			utils.Logger.Warn("load gcp general lock", zap.String("name", name), zap.Error(err))
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

func (db *GeneralDB) LoadLockByName(ctx context.Context, name string) (lock *Lock, err error) {
	utils.Logger.Debug("load lock by name", zap.String("name", name))
	docu, err := db.GetLocksCol().Doc(name).Get(ctx)
	if err != nil && docu == nil {
		utils.Logger.Error("load gcp general lock", zap.String("name", name), zap.Error(err))
		return nil, errors.Wrap(err, "load docu by name")
	}

	lock = &Lock{}
	if err = docu.DataTo(lock); err != nil {
		return nil, errors.Wrap(err, "load data to type Lock")
	}
	return lock, nil
}
