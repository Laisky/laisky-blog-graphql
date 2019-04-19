package models

import (
	"github.com/pkg/errors"
	"gopkg.in/mgo.v2"
)

type DB struct {
	s  *mgo.Session
	db *mgo.Database
}

func (d *DB) Dial(addr, dbName string) error {
	s, err := mgo.Dial(addr)
	if err != nil {
		return errors.Wrap(err, "can not connect to db")
	}

	d.s = s
	d.db = d.s.DB(dbName)
	return nil
}

func (d *DB) Close() {
	d.s.Close()
}

func (d *DB) GetCol(colName string) *mgo.Collection {
	return d.db.C(colName)
}
