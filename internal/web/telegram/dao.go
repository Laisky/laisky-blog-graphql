package telegram

import (
	"fmt"

	"laisky-blog-graphql/library/log"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	tb "gopkg.in/tucnak/telebot.v2"
)

func (s *Service) CreateOrGetUser(user *tb.User) (u *Users, err error) {
	var info *mgo.ChangeInfo
	if info, err = s.GetUsersCol().Upsert(
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
	if err = s.GetUsersCol().Find(bson.M{
		"uid": user.ID,
	}).One(u); err != nil {
		return nil, errors.Wrap(err, "load users")
	}
	if info.Matched == 0 {
		log.Logger.Info("create user",
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

func (s *Service) CreateAlertType(name string) (at *AlertTypes, err error) {
	// check if exists
	var info *mgo.ChangeInfo
	if info, err = s.GetAlertTypesCol().Upsert(
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
		return nil, ErrAlreadyExists
	}

	at = new(AlertTypes)
	if err = s.GetAlertTypesCol().Find(bson.M{
		"name": name,
	}).One(at); err != nil {
		return nil, errors.Wrap(err, "load alert_types")
	}
	if info.Matched == 0 {
		log.Logger.Info("create alert_type",
			zap.String("name", at.Name),
			zap.String("id", at.ID.Hex()))
	}

	return at, nil
}

func (s *Service) CreateOrGetUserAlertRelations(user *Users, alert *AlertTypes) (uar *UserAlertRelations, err error) {
	var info *mgo.ChangeInfo
	if info, err = s.GetUserAlertRelationsCol().Upsert(
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
	if err = s.GetUserAlertRelationsCol().Find(bson.M{
		"user_id":  user.ID,
		"alert_id": alert.ID,
	}).One(uar); err != nil {
		return nil, errors.Wrap(err, "load user_alert_relations docu")
	}
	if info.Matched == 0 {
		log.Logger.Info("create user_alert_relations",
			zap.String("user", user.Name),
			zap.String("alert_type", alert.Name),
			zap.String("id", uar.ID.Hex()))
	}

	return uar, nil
}

func (s *Service) LoadUsers(cfg *QueryCfg) (users []*Users, err error) {
	log.Logger.Debug("LoadUsers",
		zap.String("name", cfg.Name),
		zap.Int("page", cfg.Page),
		zap.Int("size", cfg.Size))

	if cfg.Size > 200 || cfg.Size < 0 {
		return nil, fmt.Errorf("size shoule in [0~200]")
	}

	users = []*Users{}
	if err = s.GetUsersCol().Find(bson.M{
		"name": cfg.Name,
	}).
		Skip(cfg.Page * cfg.Size).
		Limit(cfg.Size).
		All(&users); err != nil {
		return nil, errors.Wrap(err, "load users from db")
	}

	return users, nil
}

func (s *Service) LoadAlertTypes(cfg *QueryCfg) (alerts []*AlertTypes, err error) {
	log.Logger.Debug("LoadAlertTypes",
		zap.String("name", cfg.Name),
		zap.Int("page", cfg.Page),
		zap.Int("size", cfg.Size))

	if cfg.Size > 200 || cfg.Size < 0 {
		return nil, fmt.Errorf("size shoule in [0~200]")
	}

	alerts = []*AlertTypes{}
	if err = s.GetAlertTypesCol().Find(bson.M{
		"name": cfg.Name,
	}).
		Skip(cfg.Page * cfg.Size).
		Limit(cfg.Size).
		All(&alerts); err != nil {
		return nil, errors.Wrap(err, "load alert_types from db")
	}

	return alerts, nil
}

func (s *Service) LoadAlertTypesByUser(u *Users) (alerts []*AlertTypes, err error) {
	log.Logger.Debug("LoadAlertTypesByUser",
		zap.String("uid", u.ID.Hex()),
		zap.String("username", u.Name))

	alerts = []*AlertTypes{}
	uar := new(UserAlertRelations)
	iter := s.GetUserAlertRelationsCol().Find(bson.M{
		"user_id": u.ID,
	}).Iter()
	for iter.Next(uar) {
		alert := new(AlertTypes)
		if err = s.GetAlertTypesCol().FindId(uar.AlertMongoID).One(alert); err == mgo.ErrNotFound {
			log.Logger.Warn("can not find alert_types by user_alert_relations",
				zap.String("user_alert_relation_id", uar.ID.Hex()))
			continue
		} else if err != nil {
			return nil, errors.Wrap(err, "load alert_type by user_alert_relations")
		}
		alerts = append(alerts, alert)
	}

	return alerts, nil
}

func (s *Service) LoadUsersByAlertType(a *AlertTypes) (users []*Users, err error) {
	log.Logger.Debug("LoadUsersByAlertType",
		zap.String("alert_type", a.ID.Hex()))

	users = []*Users{}
	uar := new(UserAlertRelations)
	iter := s.GetUserAlertRelationsCol().Find(bson.M{
		"alert_id": a.ID,
	}).Iter()
	for iter.Next(uar) {
		user := new(Users)
		if err = s.GetUsersCol().FindId(uar.UserMongoID).One(user); err == mgo.ErrNotFound {
			log.Logger.Warn("can not find user by user_alert_relations",
				zap.String("user_alert_relation_id", uar.ID.Hex()))
			continue
		} else if err != nil {
			return nil, errors.Wrap(err, "load alert_type by user_alert_relations")
		}
		users = append(users, user)
	}

	return users, nil
}

func (s *Service) ValidateTokenForAlertType(token, alertType string) (alert *AlertTypes, err error) {
	log.Logger.Debug("ValidateTokenForAlertType", zap.String("alert_type", alertType))

	alert = new(AlertTypes)
	if err = s.GetAlertTypesCol().Find(bson.M{
		"name": alertType,
	}).One(alert); err == mgo.ErrNotFound {
		return nil, errors.Wrapf(err, "alert_type `%s` not found", alertType)
	} else if err != nil {
		return nil, errors.Wrapf(err, "load alert_type `%s` from db", alertType)
	}

	if token != alert.PushToken {
		return nil, fmt.Errorf("token invalidate for `%s`", alertType)
	}

	return alert, nil
}

func (s *Service) RegisterUserAlertRelation(u *Users, alertName string, joinKey string) (uar *UserAlertRelations, err error) {
	log.Logger.Info("RegisterUserAlertRelation", zap.Int("uid", u.UID), zap.String("alert", alertName))
	alert := new(AlertTypes)
	if err = s.GetAlertTypesCol().Find(bson.M{"name": alertName}).One(alert); err == mgo.ErrNotFound {
		return nil, fmt.Errorf("alert_type not found")
	} else if err != nil {
		return nil, errors.Wrap(err, "load alert_type by name: "+alertName)
	}

	if alert.JoinKey != joinKey {
		return nil, fmt.Errorf("join_key invalidate")
	}

	return s.CreateOrGetUserAlertRelations(u, alert)
}

func (s *Service) LoadUserByUID(telegramUID int) (u *Users, err error) {
	log.Logger.Debug("LoadUserByUID", zap.Int("uid", telegramUID))
	u = new(Users)
	if err = s.GetUsersCol().Find(bson.M{
		"uid": telegramUID,
	}).One(u); err == mgo.ErrNotFound {
		return nil, fmt.Errorf("not found user by uid")
	} else if err != nil {
		return nil, errors.Wrap(err, "load user in db by uid")
	}

	return u, nil
}

func (s *Service) IsUserSubAlert(uid int, alertName string) (alert *AlertTypes, err error) {
	log.Logger.Debug("IsUserSubAlert", zap.Int("uid", uid), zap.String("alert", alertName))
	alert = new(AlertTypes)
	if err = s.GetAlertTypesCol().Find(bson.M{"name": alertName}).One(alert); err != nil {
		return
	}

	u := new(Users)
	if err = s.GetUsersCol().Find(bson.M{"uid": uid}).One(u); err != nil {
		return
	}

	uar := new(UserAlertRelations)
	if err = s.GetUserAlertRelationsCol().Find(bson.M{
		"user_id":  u.ID,
		"alert_id": alert.ID,
	}).One(uar); err != nil {
		return
	}

	return alert, nil
}

func (s *Service) RefreshAlertTokenAndKey(alert *AlertTypes) (err error) {
	log.Logger.Info("RefreshAlertTokenAndKey", zap.String("alert", alert.Name))
	alert.PushToken = generatePushToken()
	alert.JoinKey = generateJoinKey()
	return s.GetAlertTypesCol().UpdateId(
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

func (s *Service) RemoveUAR(uid int, alertName string) (err error) {
	log.Logger.Info("remove user_alert_relation", zap.Int("uid", uid), zap.String("alert", alertName))
	alert := new(AlertTypes)
	if err = s.GetAlertTypesCol().Find(bson.M{"name": alertName}).One(alert); err != nil {
		return
	}

	u := new(Users)
	if err = s.GetUsersCol().Find(bson.M{"uid": uid}).One(u); err != nil {
		return
	}

	return s.GetUserAlertRelationsCol().Remove(bson.M{
		"user_id":  u.ID,
		"alert_id": alert.ID,
	})
}
