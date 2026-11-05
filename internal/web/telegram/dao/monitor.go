// Package dao is a data access object for telegram monitor.
package dao

import (
	"context"
	"sync/atomic"
	"time"

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

// ErrAlertUnauthorized matches an alert type and push token that do not
// authenticate. A storage failure during the check does not match it, so a
// caller can tell a refused credential from an outage.
var ErrAlertUnauthorized = errors.New("telegram alert credentials refused")

// alertRefusalError marks a credential refusal and keeps the wrapped error's message,
// so existing GraphQL error text and classification are unchanged.
type alertRefusalError struct{ error }

// Unwrap returns the original refusal error.
func (e alertRefusalError) Unwrap() error { return e.error }

// Is reports whether target is ErrAlertUnauthorized.
func (alertRefusalError) Is(target error) bool { return target == ErrAlertUnauthorized }

// AlertTypeNotFoundError returns the refusal ValidateTokenForAlertType gives for
// an alert type that does not exist, so a caller or test double that must
// reproduce that refusal keeps its text and its ErrAlertUnauthorized match.
func AlertTypeNotFoundError(alertType string) error {
	return alertTypeNotFound(alertType, mongoLib.ErrNoDocuments)
}

// AlertTokenMismatchError returns the refusal ValidateTokenForAlertType gives for
// a push token that does not match alertType's.
func AlertTokenMismatchError(alertType string) error {
	return alertRefusalError{errors.Errorf("token invalidate for `%s`", alertType)}
}

// alertTypeNotFound wraps cause, the storage miss, as the refusal for alertType.
func alertTypeNotFound(alertType string, cause error) error {
	return alertRefusalError{errors.Wrapf(cause, "alert_type `%s` not found", alertType)}
}

// alertCredentialChanges counts the alert types this process created or re-keyed.
var alertCredentialChanges atomic.Uint64

// AlertCredentialChanges returns how many alert types this process has created or
// re-keyed. An in-memory copy of the credentials read before the count last moved
// may lack one of them, so it must reread before refusing a credential.
func AlertCredentialChanges() uint64 {
	return alertCredentialChanges.Load()
}

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
	var name string
	name, err = sanitizeOptionalText(user.Username, maxMonitorNameLen, "user name")
	if err != nil {
		return nil, errors.Wrap(err, "sanitize user name")
	}
	info, err := d.GetUsersCol().UpdateOne(ctx,
		bson.M{fieldUid: user.ID},
		bson.M{mongoSetOnInsert: bson.M{
			fieldCreatedAt:  utils.Clock.GetUTCNow(),
			fieldModifiedAt: utils.Clock.GetUTCNow(),
			fieldName:       name,
			fieldUid:        user.ID,
		}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return nil, errors.Wrap(err, "upsert user docu")
	}

	u = new(model.MonitorUsers)
	if err = d.GetUsersCol().FindOne(ctx, bson.M{
		fieldUid: user.ID,
	}).Decode(u); err != nil {
		return nil, errors.Wrap(err, "load users")
	}

	if info.MatchedCount == 0 {
		logger.Info("create user",
			zap.String(fieldName, u.Name),
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
	if name, err = sanitizeAlertName(name); err != nil {
		return nil, errors.Wrap(err, "sanitize alert name")
	}
	// check if exists
	info, err := d.GetAlertTypesCol().UpdateOne(ctx,
		bson.M{fieldName: name},
		bson.M{mongoSetOnInsert: bson.M{
			fieldName:       name,
			fieldPushToken:  generatePushToken(),
			"join_key":      generateJoinKey(),
			fieldCreatedAt:  utils.Clock.GetUTCNow(),
			fieldModifiedAt: utils.Clock.GetUTCNow(),
		}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		// The upsert may still have inserted the alert type.
		alertCredentialChanges.Add(1)
		return nil, errors.Wrap(err, "upsert alert_types docu")
	}
	if info.MatchedCount != 0 {
		return nil, errors.New("already exists")
	}
	alertCredentialChanges.Add(1)

	at = new(model.AlertTypes)
	if err = d.GetAlertTypesCol().FindOne(ctx, bson.M{
		fieldName: name,
	}).Decode(at); err != nil {
		return nil, errors.Wrap(err, "load alert_types")
	}
	if info.MatchedCount == 0 {
		logger.Info("create alert_type",
			zap.String(fieldName, at.Name),
			zap.String("id", at.ID.Hex()))
	}

	return at, nil
}

func (d *Monitor) CreateOrGetUserAlertRelations(ctx context.Context, user *model.MonitorUsers,
	alert *model.AlertTypes) (
	uar *model.UserAlertRelations,
	err error) {
	info, err := d.GetUserAlertRelationsCol().UpdateOne(ctx,
		bson.M{fieldUserId: user.ID, fieldAlertId: alert.ID},
		bson.M{
			mongoSetOnInsert: bson.M{
				fieldUserId:     user.ID,
				fieldAlertId:    alert.ID,
				fieldCreatedAt:  utils.Clock.GetUTCNow(),
				fieldModifiedAt: utils.Clock.GetUTCNow(),
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
		fieldUserId:  user.ID,
		fieldAlertId: alert.ID,
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
	if cfg, err = sanitizeQueryCfg(cfg); err != nil {
		return nil, errors.Wrap(err, "sanitize query config")
	}
	logger.Debug("LoadUsers",
		zap.String(fieldName, cfg.Name),
		zap.Int("page", cfg.Page),
		zap.Int("size", cfg.Size))

	users = []*model.MonitorUsers{}
	filter := bson.M{}
	if cfg.Name != "" {
		filter[fieldName] = cfg.Name
	}
	cur, err := d.GetUsersCol().Find(ctx,
		filter,
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
	if cfg, err = sanitizeQueryCfg(cfg); err != nil {
		return nil, errors.Wrap(err, "sanitize query config")
	}
	logger.Debug("LoadAlertTypes",
		zap.String(fieldName, cfg.Name),
		zap.Int("page", cfg.Page),
		zap.Int("size", cfg.Size))

	alerts = []*model.AlertTypes{}
	filter := bson.M{}
	if cfg.Name != "" {
		filter[fieldName] = cfg.Name
	}
	cur, err := d.GetAlertTypesCol().Find(ctx,
		filter,
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
		zap.String(fieldUid, u.ID.Hex()),
		zap.String("username", u.Name))

	alerts = []*model.AlertTypes{}
	iter, err := d.GetUserAlertRelationsCol().Find(ctx,
		bson.M{
			fieldUserId: u.ID,
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
			FindOne(ctx, bson.D{{Key: fieldDocumentID, Value: uar.AlertMongoID}}).
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
			fieldAlertId: a.ID,
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
		if err = d.GetUsersCol().FindOne(ctx, bson.D{{Key: fieldDocumentID, Value: uar.UserMongoID}}).
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
	if alertType, err = sanitizeAlertName(alertType); err != nil {
		return nil, alertRefusalError{errors.Wrap(err, "sanitize alert type")}
	}
	if token, err = sanitizePushToken(token); err != nil {
		return nil, alertRefusalError{errors.Wrap(err, "sanitize push token")}
	}
	logger.Debug("ValidateTokenForAlertType", zap.String("alert_type", alertType))

	alert = new(model.AlertTypes)
	if err = d.GetAlertTypesCol().FindOne(ctx,
		bson.M{
			fieldName: alertType,
		}).Decode(alert); mongo.NotFound(err) {
		return nil, alertTypeNotFound(alertType, err)
	} else if err != nil {
		return nil, errors.Wrapf(err, "load alert_type `%s` from db", alertType)
	}

	if !secureCompareString(token, alert.PushToken) {
		return nil, AlertTokenMismatchError(alertType)
	}

	return alert, nil
}

// LoadAlertCredentials returns the name and push token of up to limit alert
// types, oldest first, and reports whether that is every alert type. Only those
// two fields are read. A document whose name or push token is not a string is
// skipped, because ValidateTokenForAlertType can never accept it either.
func (d *Monitor) LoadAlertCredentials(ctx context.Context, limit int) (alerts []*model.AlertTypes, complete bool, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_load_alert_credentials")
	if limit <= 0 {
		return nil, false, errors.Errorf("alert credential limit must be positive, got %d", limit)
	}
	cur, err := d.GetAlertTypesCol().Find(ctx, bson.M{},
		options.Find().
			SetProjection(bson.M{fieldDocumentID: 0, fieldName: 1, fieldPushToken: 1}).
			SetSort(bson.D{{Key: fieldDocumentID, Value: 1}}).
			SetLimit(int64(limit)+1),
	)
	if err != nil {
		return nil, false, errors.Wrap(err, "find alert credentials")
	}
	defer func() {
		if closeErr := closeCursor(ctx, cur); closeErr != nil && err == nil {
			err = errors.Wrap(closeErr, "close alert credentials cursor")
		}
	}()

	var read, skipped int
	alerts = []*model.AlertTypes{}
	for cur.Next(ctx) {
		read++
		if read > limit {
			break
		}
		var credential struct {
			Name      string `bson:"name"`
			PushToken string `bson:"push_token"`
		}
		if err = cur.Decode(&credential); err != nil {
			skipped++
			continue
		}
		alerts = append(alerts, &model.AlertTypes{Name: credential.Name, PushToken: credential.PushToken})
	}
	if err = cur.Err(); err != nil {
		return nil, false, errors.Wrap(err, "iterate alert credentials")
	}
	if skipped > 0 {
		logger.Warn("skip malformed alert types", zap.Int("skipped", skipped))
	}
	return alerts, read <= limit, nil
}

// cursorCloseTimeout bounds closing a cursor that was not read to its end,
// which costs a killCursors round trip. A server cursor that is never killed
// still closes on its own after cursorTimeoutMillis (10 minutes by default).
const cursorCloseTimeout = 2 * time.Second

// cursorCloser is the part of *mongo.Cursor that closeCursor uses.
type cursorCloser interface {
	Close(ctx context.Context) error
}

// closeCursor closes cur under a deadline of its own. The deadline does not
// end with ctx, so a read cut short by its deadline or by cancellation still
// kills its server cursor. But it is short and fixed, because
// context.WithoutCancel alone has no deadline: on a stalled connection the close
// would then block until the driver's own connection timeouts, long after the
// read's deadline.
func closeCursor(ctx context.Context, cur cursorCloser) error {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cursorCloseTimeout)
	defer cancel()
	return errors.WithStack(cur.Close(closeCtx))
}

func (d *Monitor) RegisterUserAlertRelation(ctx context.Context,
	u *model.MonitorUsers,
	alertName string,
	joinKey string,
) (uar *model.UserAlertRelations, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_register_uar")
	if alertName, err = sanitizeAlertName(alertName); err != nil {
		return nil, errors.Wrap(err, "sanitize alert name")
	}
	if joinKey, err = sanitizeJoinKey(joinKey); err != nil {
		return nil, errors.Wrap(err, "sanitize join key")
	}
	logger.Info("RegisterUserAlertRelation", zap.Int(fieldUid, u.UID), zap.String("alert", alertName))
	alert := new(model.AlertTypes)
	if err = d.GetAlertTypesCol().
		FindOne(ctx, bson.M{fieldName: alertName}).
		Decode(alert); mongo.NotFound(err) {
		return nil, errors.Errorf("alert_type not found")
	} else if err != nil {
		return nil, errors.Wrap(err, "load alert_type by name: "+alertName)
	}

	if !secureCompareString(alert.JoinKey, joinKey) {
		return nil, errors.Errorf("join_key invalidate")
	}

	return d.CreateOrGetUserAlertRelations(ctx, u, alert)
}

func (d *Monitor) LoadUserByUID(ctx context.Context, telegramUID int) (u *model.MonitorUsers, err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_load_user_by_uid")
	logger.Debug("LoadUserByUID", zap.Int(fieldUid, telegramUID))
	u = new(model.MonitorUsers)
	if err = d.GetUsersCol().FindOne(ctx,
		bson.M{
			fieldUid: telegramUID,
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
	if alertName, err = sanitizeAlertName(alertName); err != nil {
		return nil, errors.Wrap(err, "sanitize alert name")
	}
	logger.Debug("IsUserSubAlert", zap.Int(fieldUid, uid), zap.String("alert", alertName))
	alert = new(model.AlertTypes)
	if err = d.GetAlertTypesCol().FindOne(ctx, bson.M{fieldName: alertName}).Decode(alert); err != nil {
		return
	}

	u := new(model.MonitorUsers)
	if err = d.GetUsersCol().FindOne(ctx, bson.M{fieldUid: uid}).Decode(u); err != nil {
		return
	}

	uar := new(model.UserAlertRelations)
	if err = d.GetUserAlertRelationsCol().FindOne(ctx,
		bson.M{
			fieldUserId:  u.ID,
			fieldAlertId: alert.ID,
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
		bson.D{{Key: fieldDocumentID, Value: alert.ID}},
		bson.M{
			mongoSet: bson.M{
				fieldPushToken:  alert.PushToken,
				"join_key":      alert.JoinKey,
				fieldModifiedAt: utils.Clock.GetUTCNow(),
			},
		},
	)
	// Counted even after an error, since the update may still have applied.
	alertCredentialChanges.Add(1)
	return errors.Wrap(err, "update alert token")
}

func (d *Monitor) RemoveUAR(ctx context.Context, uid int, alertName string) (err error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_remove_uar")
	if alertName, err = sanitizeAlertName(alertName); err != nil {
		return errors.Wrap(err, "sanitize alert name")
	}
	logger.Info("remove user_alert_relation", zap.Int(fieldUid, uid), zap.String("alert", alertName))
	alert := new(model.AlertTypes)
	if err = d.GetAlertTypesCol().
		FindOne(ctx, bson.M{fieldName: alertName}).
		Decode(alert); err != nil {
		return
	}

	u := new(model.MonitorUsers)
	if err = d.GetUsersCol().FindOne(ctx, bson.M{fieldUid: uid}).Decode(u); err != nil {
		return
	}

	_, err = d.GetUserAlertRelationsCol().DeleteMany(ctx,
		bson.M{
			fieldUserId:  u.ID,
			fieldAlertId: alert.ID,
		})

	return errors.Wrapf(err, "delete user %d alert %s rel", uid, alertName)
}
