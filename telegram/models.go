package telegram

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/models"
	"github.com/pkg/errors"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	tb "gopkg.in/tucnak/telebot.v2"
)

const (
	monitorDBName            = "monitor"
	alertTypeColName         = "alert_types"
	usersColName             = "users"
	userAlertRelationColName = "user_alert_relations"
)

var (
	AlreadyExistsErr = errors.New("already exists")
)

type TelegramQueryCfg struct {
	Name       string
	Page, Size int
}

// MonitorDB db
type MonitorDB struct {
	dbcli *models.DB
}

// NewMonitorDB create new MonitorDB
func NewMonitorDB(dbcli *models.DB) *MonitorDB {
	return &MonitorDB{
		dbcli: dbcli,
	}
}

// AlertTypes type of alert
type AlertTypes struct {
	ID         bson.ObjectId `bson:"_id,omitempty" json:"mongo_id"`
	Name       string        `bson:"name" json:"name"`
	PushToken  string        `bson:"push_token" json:"push_token"`
	JoinKey    string        `bson:"join_key" json:"join_key"`
	CreatedAt  time.Time     `bson:"created_at" json:"created_at"`
	ModifiedAt time.Time     `bson:"modified_at" json:"modified_at"`
}

type Users struct {
	ID         bson.ObjectId `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt  time.Time     `bson:"created_at" json:"created_at"`
	ModifiedAt time.Time     `bson:"modified_at" json:"modified_at"`
	Name       string        `bson:"name" json:"name"`
	UID        int           `bson:"uid" json:"uid"`
}

type UserAlertRelations struct {
	ID           bson.ObjectId `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt    time.Time     `bson:"created_at" json:"created_at"`
	ModifiedAt   time.Time     `bson:"modified_at" json:"modified_at"`
	UserMongoID  bson.ObjectId `bson:"user_id" json:"user_id"`
	AlertMongoID bson.ObjectId `bson:"alert_id" json:"alert_id"`
}

func (db *MonitorDB) GetAlertTypesCol() *mgo.Collection {
	return db.dbcli.GetCol(alertTypeColName)
}
func (db *MonitorDB) GetUsersCol() *mgo.Collection {
	return db.dbcli.GetCol(usersColName)
}
func (db *MonitorDB) GetUserAlertRelationsCol() *mgo.Collection {
	return db.dbcli.GetCol(userAlertRelationColName)
}

func (db *MonitorDB) CreateOrGetUser(user *tb.User) (u *Users, err error) {
	var info *mgo.ChangeInfo
	if info, err = db.GetUsersCol().Upsert(
		bson.M{"uid": user.ID},
		bson.M{"$setOnInsert": bson.M{
			"created_at":  utils.Clock.GetUTCNow(),
			"modified_at": utils.Clock.GetUTCNow(),
			"name":        user.Username,
			"uid":         user.ID,
		}}); err != nil {
		return nil, errors.Wrap(err, "upsert user docu")
	}

	u = new(Users)
	if err = db.GetUsersCol().Find(bson.M{
		"uid": user.ID,
	}).One(u); err != nil {
		return nil, errors.Wrap(err, "load users")
	}
	if info.Matched == 0 {
		utils.Logger.Info("create user",
			zap.String("name", u.Name),
			zap.String("id", u.ID.Hex()))
	}

	return u, nil
}

func generatePushToken() string {
	return utils.RandomStringWithLength(20)
}

func generateJoinKey() string {
	return utils.RandomStringWithLength(6)
}

func (db *MonitorDB) CreateAlertType(name string) (at *AlertTypes, err error) {
	// check if exists
	var info *mgo.ChangeInfo
	if info, err = db.GetAlertTypesCol().Upsert(
		bson.M{"name": name},
		bson.M{"$setOnInsert": bson.M{
			"name":        name,
			"push_token":  generatePushToken(),
			"join_key":    generateJoinKey(),
			"created_at":  utils.Clock.GetUTCNow(),
			"modified_at": utils.Clock.GetUTCNow(),
		}},
	); err != nil {
		return nil, errors.Wrap(err, "upsert alert_types docu")
	}
	if info.Matched != 0 {
		return nil, AlreadyExistsErr
	}

	at = new(AlertTypes)
	if err = db.GetAlertTypesCol().Find(bson.M{
		"name": name,
	}).One(at); err != nil {
		return nil, errors.Wrap(err, "load alert_types")
	}
	if info.Matched == 0 {
		utils.Logger.Info("create alert_type",
			zap.String("name", at.Name),
			zap.String("id", at.ID.Hex()))
	}

	return at, nil
}

func (db *MonitorDB) CreateOrGetUserAlertRelations(user *Users, alert *AlertTypes) (uar *UserAlertRelations, err error) {
	var info *mgo.ChangeInfo
	if info, err = db.GetUserAlertRelationsCol().Upsert(
		bson.M{"user_id": user.ID, "alert_id": alert.ID},
		bson.M{
			"$setOnInsert": bson.M{
				"user_id":     user.ID,
				"alert_id":    alert.ID,
				"created_at":  utils.Clock.GetUTCNow(),
				"modified_at": utils.Clock.GetUTCNow(),
			}},
	); err != nil {
		return nil, errors.Wrap(err, "upsert user_alert_relations docu")
	}
	// if info.Matched != 0 {
	// 	return nil, AlreadyExistsErr
	// }

	uar = new(UserAlertRelations)
	if err = db.GetUserAlertRelationsCol().Find(bson.M{
		"user_id":  user.ID,
		"alert_id": alert.ID,
	}).One(uar); err != nil {
		return nil, errors.Wrap(err, "load user_alert_relations docu")
	}
	if info.Matched == 0 {
		utils.Logger.Info("create user_alert_relations",
			zap.String("user", user.Name),
			zap.String("alert_type", alert.Name),
			zap.String("id", uar.ID.Hex()))
	}

	return uar, nil
}

func (db *MonitorDB) LoadUsers(cfg *TelegramQueryCfg) (users []*Users, err error) {
	utils.Logger.Debug("LoadUsers",
		zap.String("name", cfg.Name),
		zap.Int("page", cfg.Page),
		zap.Int("size", cfg.Size))

	if cfg.Size > 200 || cfg.Size < 0 {
		return nil, fmt.Errorf("size shoule in [0~200]")
	}

	users = []*Users{}
	if err = db.GetUsersCol().Find(bson.M{
		"name": cfg.Name,
	}).
		Skip(cfg.Page * cfg.Size).
		Limit(cfg.Size).
		All(&users); err != nil {
		return nil, errors.Wrap(err, "load users from db")
	}

	return users, nil
}

func (db *MonitorDB) LoadAlertTypes(cfg *TelegramQueryCfg) (alerts []*AlertTypes, err error) {
	utils.Logger.Debug("LoadAlertTypes",
		zap.String("name", cfg.Name),
		zap.Int("page", cfg.Page),
		zap.Int("size", cfg.Size))

	if cfg.Size > 200 || cfg.Size < 0 {
		return nil, fmt.Errorf("size shoule in [0~200]")
	}

	alerts = []*AlertTypes{}
	if err = db.GetAlertTypesCol().Find(bson.M{
		"name": cfg.Name,
	}).
		Skip(cfg.Page * cfg.Size).
		Limit(cfg.Size).
		All(&alerts); err != nil {
		return nil, errors.Wrap(err, "load alert_types from db")
	}

	return alerts, nil
}

func (db *MonitorDB) LoadAlertTypesByUser(u *Users) (alerts []*AlertTypes, err error) {
	utils.Logger.Debug("LoadAlertTypesByUser",
		zap.String("uid", u.ID.Hex()),
		zap.String("username", u.Name))

	alerts = []*AlertTypes{}
	uar := new(UserAlertRelations)
	iter := db.GetUserAlertRelationsCol().Find(bson.M{
		"user_id": u.ID,
	}).Iter()
	for iter.Next(uar) {
		alert := new(AlertTypes)
		if err = db.GetAlertTypesCol().FindId(uar.AlertMongoID).One(alert); err == mgo.ErrNotFound {
			utils.Logger.Warn("can not find alert_types by user_alert_relations",
				zap.String("user_alert_relation_id", uar.ID.Hex()))
			continue
		} else if err != nil {
			return nil, errors.Wrap(err, "load alert_type by user_alert_relations")
		}
		alerts = append(alerts, alert)
	}

	return alerts, nil
}

func (db *MonitorDB) LoadUsersByAlertType(a *AlertTypes) (users []*Users, err error) {
	utils.Logger.Debug("LoadUsersByAlertType",
		zap.String("alert_type", a.ID.Hex()))

	users = []*Users{}
	uar := new(UserAlertRelations)
	iter := db.GetUserAlertRelationsCol().Find(bson.M{
		"alert_id": a.ID,
	}).Iter()
	for iter.Next(uar) {
		user := new(Users)
		if err = db.GetUsersCol().FindId(uar.UserMongoID).One(user); err == mgo.ErrNotFound {
			utils.Logger.Warn("can not find user by user_alert_relations",
				zap.String("user_alert_relation_id", uar.ID.Hex()))
			continue
		} else if err != nil {
			return nil, errors.Wrap(err, "load alert_type by user_alert_relations")
		}
		users = append(users, user)
	}

	return users, nil
}

func (db *MonitorDB) ValidateTokenForAlertType(token, alert_type string) (alert *AlertTypes, err error) {
	utils.Logger.Debug("ValidateTokenForAlertType", zap.String("alert_type", alert_type))

	alert = new(AlertTypes)
	if err = db.GetAlertTypesCol().Find(bson.M{
		"name": alert_type,
	}).One(alert); err == mgo.ErrNotFound {
		return nil, fmt.Errorf("alert_type not found")
	} else if err != nil {
		return nil, errors.Wrap(err, "load alert_type from db")
	}

	if token != alert.PushToken {
		return nil, fmt.Errorf("token invalidate")
	}

	return alert, nil
}

func (db *MonitorDB) RegisterUserAlertRelation(uid int, alertName string, joinKey string) (uar *UserAlertRelations, err error) {
	utils.Logger.Info("RegisterUserAlertRelation", zap.Int("uid", uid), zap.String("alert", alertName))
	u := new(Users)
	if err = db.GetUsersCol().Find(bson.M{"uid": uid}).One(u); err == mgo.ErrNotFound {
		return nil, fmt.Errorf("user not found")
	} else if err != nil {
		return nil, errors.Wrap(err, "load user by uid: "+strconv.FormatInt(int64(uid), 10))
	}

	alert := new(AlertTypes)
	if err = db.GetAlertTypesCol().Find(bson.M{"name": alertName}).One(alert); err == mgo.ErrNotFound {
		return nil, fmt.Errorf("alert_type not found")
	} else if err != nil {
		return nil, errors.Wrap(err, "load alert_type by name: "+alertName)
	}

	if alert.JoinKey != joinKey {
		return nil, fmt.Errorf("join_key invalidate")
	}

	return db.CreateOrGetUserAlertRelations(u, alert)
}

func (db *MonitorDB) LoadUserByUID(telegramUID int) (u *Users, err error) {
	utils.Logger.Debug("LoadUserByUID", zap.Int("uid", telegramUID))
	u = new(Users)
	if err = db.GetUsersCol().Find(bson.M{
		"uid": telegramUID,
	}).One(u); err == mgo.ErrNotFound {
		return nil, fmt.Errorf("not found user by uid")
	} else if err != nil {
		return nil, errors.Wrap(err, "load user in db by uid")
	}

	return u, nil
}

func (db *MonitorDB) IsUserSubAlert(uid int, alertName string) (alert *AlertTypes, err error) {
	utils.Logger.Debug("IsUserSubAlert", zap.Int("uid", uid), zap.String("alert", alertName))
	alert = new(AlertTypes)
	if err = db.GetAlertTypesCol().Find(bson.M{"name": alertName}).One(alert); err != nil {
		return
	}

	u := new(Users)
	if err = db.GetUsersCol().Find(bson.M{"uid": uid}).One(u); err != nil {
		return
	}

	uar := new(UserAlertRelations)
	if err = db.GetUserAlertRelationsCol().Find(bson.M{
		"user_id":  u.ID,
		"alert_id": alert.ID,
	}).One(uar); err != nil {
		return
	}

	return alert, nil
}

func (db *MonitorDB) RefreshAlertTokenAndKey(alert *AlertTypes) (err error) {
	utils.Logger.Info("RefreshAlertTokenAndKey", zap.String("alert", alert.Name))
	alert.PushToken = generatePushToken()
	alert.JoinKey = generateJoinKey()
	return db.GetAlertTypesCol().UpdateId(
		alert.ID,
		bson.M{
			"$set": bson.M{
				"push_token":  alert.PushToken,
				"join_key":    alert.JoinKey,
				"modified_at": utils.Clock.GetUTCNow(),
			},
		},
	)
}

func (db *MonitorDB) RemoveUAR(uid int, alertName string) (err error) {
	utils.Logger.Info("remove user_alert_relation", zap.Int("uid", uid), zap.String("alert", alertName))
	alert := new(AlertTypes)
	if err = db.GetAlertTypesCol().Find(bson.M{"name": alertName}).One(alert); err != nil {
		return
	}

	u := new(Users)
	if err = db.GetUsersCol().Find(bson.M{"uid": uid}).One(u); err != nil {
		return
	}

	return db.GetUserAlertRelationsCol().Remove(bson.M{
		"user_id":  u.ID,
		"alert_id": alert.ID,
	})
}
