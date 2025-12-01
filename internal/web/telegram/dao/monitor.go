// Package dao is a data access object for telegram monitor.
package dao

import (
	"context"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	tb "gopkg.in/telebot.v3"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
)

const (
	alertTypeColName         = "alert_types"
	usersColName             = "users"
	userAlertRelationColName = "user_alert_relations"
)

// Monitor db
type Monitor struct {
	db mongo.DB
}

// NewMonitor create new DB
func NewMonitor(db mongo.DB) *Monitor {
	return &Monitor{db}
}

func (d *Monitor) GetAlertTypesCol() *mongoLib.Collection {
	return d.db.GetCol(alertTypeColName)
}
func (d *Monitor) GetUsersCol() *mongoLib.Collection {
	return d.db.GetCol(usersColName)
}
func (d *Monitor) GetUserAlertRelationsCol() *mongoLib.Collection {
	return d.db.GetCol(userAlertRelationColName)
}

func (d *Monitor) CreateOrGetUser(ctx context.Context, user *tb.User) (u *model.MonitorUsers, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_create_user")
	info, err := d.GetUsersCol().UpdateOne(ctx,
		bson.M{"uid": user.ID},
		bson.M{"$setOnInsert": bson.M{
			"created_at":  utils.Clock.GetUTCNow(),
			"modified_at": utils.Clock.GetUTCNow(),
			"name":        user.Username,
			"uid":         user.ID,
		}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return nil, errors.Wrap(err, "upsert user docu")
	}

	u = new(model.MonitorUsers)
	if err = d.GetUsersCol().FindOne(ctx, bson.M{
		"uid": user.ID,
	}).Decode(u); err != nil {
		return nil, errors.Wrap(err, "load users")
	}

	if info.MatchedCount == 0 {
		logger.Info("create user",
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

func (d *Monitor) CreateAlertType(ctx context.Context, name string) (at *model.AlertTypes, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_create_alert")
	// check if exists
	info, err := d.GetAlertTypesCol().UpdateOne(ctx,
		bson.M{"name": name},
		bson.M{"$setOnInsert": bson.M{
			"name":        name,
			"push_token":  generatePushToken(),
			"join_key":    generateJoinKey(),
			"created_at":  utils.Clock.GetUTCNow(),
			"modified_at": utils.Clock.GetUTCNow(),
		}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return nil, errors.Wrap(err, "upsert alert_types docu")
	}
	if info.MatchedCount != 0 {
		return nil, errors.New("already exists")
	}

	at = new(model.AlertTypes)
	if err = d.GetAlertTypesCol().FindOne(ctx, bson.M{
		"name": name,
	}).Decode(at); err != nil {
		return nil, errors.Wrap(err, "load alert_types")
	}
	if info.MatchedCount == 0 {
		logger.Info("create alert_type",
			zap.String("name", at.Name),
			zap.String("id", at.ID.Hex()))
	}

	return at, nil
}

func (d *Monitor) CreateOrGetUserAlertRelations(ctx context.Context, user *model.MonitorUsers,
	alert *model.AlertTypes) (
	uar *model.UserAlertRelations,
	err error) {
	info, err := d.GetUserAlertRelationsCol().UpdateOne(ctx,
		bson.M{"user_id": user.ID, "alert_id": alert.ID},
		bson.M{
			"$setOnInsert": bson.M{
				"user_id":     user.ID,
				"alert_id":    alert.ID,
				"created_at":  utils.Clock.GetUTCNow(),
				"modified_at": utils.Clock.GetUTCNow(),
			}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return nil, errors.Wrap(err, "upsert user_alert_relations docu")
	}
	// if info.Matched != 0 {
	// 	return nil, AlreadyExistsErr
	// }

	uar = new(model.UserAlertRelations)
	if err = d.GetUserAlertRelationsCol().FindOne(ctx, bson.M{
		"user_id":  user.ID,
		"alert_id": alert.ID,
	}).Decode(uar); err != nil {
		return nil, errors.Wrap(err, "load user_alert_relations docu")
	}
	if info.MatchedCount == 0 {
		logger := gmw.GetLogger(ctx).Named("telegram_monitor_create_uar")
		logger.Info("create user_alert_relations",
			zap.String("user", user.Name),
			zap.String("alert_type", alert.Name),
			zap.String("id", uar.ID.Hex()))
	}

	return uar, nil
}

func (d *Monitor) LoadUsers(ctx context.Context, cfg *dto.QueryCfg) (users []*model.MonitorUsers, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_load_users")
	logger.Debug("LoadUsers",
		zap.String("name", cfg.Name),
		zap.Int("page", cfg.Page),
		zap.Int("size", cfg.Size))

	if cfg.Size > 200 || cfg.Size < 0 {
		return nil, errors.Errorf("size shoule in [0~200]")
	}

	users = []*model.MonitorUsers{}
	cur, err := d.GetUsersCol().Find(ctx,
		bson.M{
			"name": cfg.Name,
		},
		options.Find().SetSkip(int64(cfg.Page*cfg.Size)),
		options.Find().SetLimit(int64(cfg.Size)),
	)
	if err != nil {
		return nil, errors.Wrap(err, "find users from db")
	}

	if err = cur.All(ctx, &users); err != nil {
		return nil, errors.Wrap(err, "load all users")
	}

	return users, nil
}

func (d *Monitor) LoadAlertTypes(ctx context.Context, cfg *dto.QueryCfg) (alerts []*model.AlertTypes, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_load_alert_types")
	logger.Debug("LoadAlertTypes",
		zap.String("name", cfg.Name),
		zap.Int("page", cfg.Page),
		zap.Int("size", cfg.Size))

	if cfg.Size > 200 || cfg.Size < 0 {
		return nil, errors.Errorf("size shoule in [0~200]")
	}

	alerts = []*model.AlertTypes{}
	cur, err := d.GetAlertTypesCol().Find(ctx,
		bson.M{
			"name": cfg.Name,
		},
		options.Find().SetSkip(int64(cfg.Page*cfg.Size)),
		options.Find().SetLimit(int64(cfg.Size)),
	)
	if err != nil {
		return nil, errors.Wrap(err, "find alert_types from db")
	}

	if err = cur.All(ctx, &alerts); err != nil {
		return nil, errors.Wrap(err, "load all alert types")
	}

	return alerts, nil
}

func (d *Monitor) LoadAlertTypesByUser(ctx context.Context, u *model.MonitorUsers) (alerts []*model.AlertTypes, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_load_alerts_by_user")
	logger.Debug("LoadAlertTypesByUser",
		zap.String("uid", u.ID.Hex()),
		zap.String("username", u.Name))

	alerts = []*model.AlertTypes{}
	iter, err := d.GetUserAlertRelationsCol().Find(ctx,
		bson.M{
			"user_id": u.ID,
		})
	if err != nil {
		return nil, errors.Wrap(err, "find alerts")
	}

	for iter.Next(ctx) {
		uar := new(model.UserAlertRelations)
		if err = iter.Decode(uar); err != nil {
			return nil, errors.Wrap(err, "load uar")
		}

		alert := new(model.AlertTypes)
		if err = d.GetAlertTypesCol().
			FindOne(ctx, bson.D{{Key: "_id", Value: uar.AlertMongoID}}).
			Decode(alert); mongo.NotFound(err) {
			logger.Warn("can not find alert_types by user_alert_relations",
				zap.String("user_alert_relation_id", uar.ID.Hex()))
			continue
		} else if err != nil {
			return nil, errors.Wrap(err, "load alert_type by user_alert_relations")
		}
		alerts = append(alerts, alert)
	}

	return alerts, nil
}

func (d *Monitor) LoadUsersByAlertType(ctx context.Context, a *model.AlertTypes) (users []*model.MonitorUsers, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_load_users_by_alert")
	logger.Debug("LoadUsersByAlertType",
		zap.String("alert_type", a.ID.Hex()))

	users = []*model.MonitorUsers{}
	iter, err := d.GetUserAlertRelationsCol().Find(ctx,
		bson.M{
			"alert_id": a.ID,
		})
	if err != nil {
		return nil, errors.Wrap(err, "find user alert rels")
	}

	for iter.Next(ctx) {
		uar := new(model.UserAlertRelations)
		if err = iter.Decode(uar); err != nil {
			return nil, errors.Wrap(err, "load user alert rel")
		}

		user := new(model.MonitorUsers)
		if err = d.GetUsersCol().FindOne(ctx, bson.D{{Key: "_id", Value: uar.UserMongoID}}).
			Decode(user); mongo.NotFound(err) {
			logger.Warn("can not find user by user_alert_relations",
				zap.String("user_alert_relation_id", uar.ID.Hex()))
			continue
		} else if err != nil {
			return nil, errors.Wrap(err, "load alert_type by user_alert_relations")
		}
		users = append(users, user)
	}

	return users, nil
}

func (d *Monitor) ValidateTokenForAlertType(ctx context.Context,
	token, alertType string) (alert *model.AlertTypes, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_validate_token")
	logger.Debug("ValidateTokenForAlertType", zap.String("alert_type", alertType))

	alert = new(model.AlertTypes)
	if err = d.GetAlertTypesCol().FindOne(ctx,
		bson.M{
			"name": alertType,
		}).Decode(alert); mongo.NotFound(err) {
		return nil, errors.Wrapf(err, "alert_type `%s` not found", alertType)
	} else if err != nil {
		return nil, errors.Wrapf(err, "load alert_type `%s` from db", alertType)
	}

	if token != alert.PushToken {
		return nil, errors.Errorf("token invalidate for `%s`", alertType)
	}

	return alert, nil
}

func (d *Monitor) RegisterUserAlertRelation(ctx context.Context,
	u *model.MonitorUsers,
	alertName string,
	joinKey string,
) (uar *model.UserAlertRelations, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_register_uar")
	logger.Info("RegisterUserAlertRelation", zap.Int("uid", u.UID), zap.String("alert", alertName))
	alert := new(model.AlertTypes)
	if err = d.GetAlertTypesCol().
		FindOne(ctx, bson.M{"name": alertName}).
		Decode(alert); mongo.NotFound(err) {
		return nil, errors.Errorf("alert_type not found")
	} else if err != nil {
		return nil, errors.Wrap(err, "load alert_type by name: "+alertName)
	}

	if alert.JoinKey != joinKey {
		return nil, errors.Errorf("join_key invalidate")
	}

	return d.CreateOrGetUserAlertRelations(ctx, u, alert)
}

func (d *Monitor) LoadUserByUID(ctx context.Context, telegramUID int) (u *model.MonitorUsers, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_load_user_by_uid")
	logger.Debug("LoadUserByUID", zap.Int("uid", telegramUID))
	u = new(model.MonitorUsers)
	if err = d.GetUsersCol().FindOne(ctx,
		bson.M{
			"uid": telegramUID,
		}).
		Decode(u); mongo.NotFound(err) {
		return nil, errors.Errorf(`not found user by uid "%d"`, telegramUID)
	} else if err != nil {
		return nil, errors.Wrap(err, "load user in db by uid")
	}

	return u, nil
}

func (d *Monitor) IsUserSubAlert(ctx context.Context, uid int, alertName string) (alert *model.AlertTypes, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_is_user_sub_alert")
	logger.Debug("IsUserSubAlert", zap.Int("uid", uid), zap.String("alert", alertName))
	alert = new(model.AlertTypes)
	if err = d.GetAlertTypesCol().FindOne(ctx, bson.M{"name": alertName}).Decode(alert); err != nil {
		return
	}

	u := new(model.MonitorUsers)
	if err = d.GetUsersCol().FindOne(ctx, bson.M{"uid": uid}).Decode(u); err != nil {
		return
	}

	uar := new(model.UserAlertRelations)
	if err = d.GetUserAlertRelationsCol().FindOne(ctx,
		bson.M{
			"user_id":  u.ID,
			"alert_id": alert.ID,
		}).Decode(uar); err != nil {
		return
	}

	return alert, nil
}

func (d *Monitor) RefreshAlertTokenAndKey(ctx context.Context, alert *model.AlertTypes) (err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_refresh_alert_token")
	logger.Info("RefreshAlertTokenAndKey", zap.String("alert", alert.Name))
	alert.PushToken = generatePushToken()
	alert.JoinKey = generateJoinKey()
	_, err = d.GetAlertTypesCol().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: alert.ID}},
		bson.M{
			"$set": bson.M{
				"push_token":  alert.PushToken,
				"join_key":    alert.JoinKey,
				"modified_at": utils.Clock.GetUTCNow(),
			},
		},
	)
	return errors.Wrap(err, "update alert token")
}

func (d *Monitor) RemoveUAR(ctx context.Context, uid int, alertName string) (err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_remove_uar")
	logger.Info("remove user_alert_relation", zap.Int("uid", uid), zap.String("alert", alertName))
	alert := new(model.AlertTypes)
	if err = d.GetAlertTypesCol().
		FindOne(ctx, bson.M{"name": alertName}).
		Decode(alert); err != nil {
		return
	}

	u := new(model.MonitorUsers)
	if err = d.GetUsersCol().FindOne(ctx, bson.M{"uid": uid}).Decode(u); err != nil {
		return
	}

	_, err = d.GetUserAlertRelationsCol().DeleteMany(ctx,
		bson.M{
			"user_id":  u.ID,
			"alert_id": alert.ID,
		})

	return errors.Wrapf(err, "delete user %d alert %s rel", uid, alertName)
}
