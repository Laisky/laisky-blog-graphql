package db

import (
	"laisky-blog-graphql/library/db"

	"gopkg.in/mgo.v2"
)

const (
	alertTypeColName         = "alert_types"
	usersColName             = "users"
	userAlertRelationColName = "user_alert_relations"
)

// DB db
type DB struct {
	*db.DB
}

// NewMoNewDBnitorDB create new DB
func NewDB(dbcli *db.DB) *DB {
	return &DB{dbcli}
}

func (db *DB) GetAlertTypesCol() *mgo.Collection {
	return db.GetCol(alertTypeColName)
}
func (db *DB) GetUsersCol() *mgo.Collection {
	return db.GetCol(usersColName)
}
func (db *DB) GetUserAlertRelationsCol() *mgo.Collection {
	return db.GetCol(userAlertRelationColName)
}
