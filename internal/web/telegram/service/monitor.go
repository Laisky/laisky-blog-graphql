package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v4"
	"github.com/Laisky/zap"
	tb "gopkg.in/telebot.v3"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

func (s *Type) monitorHandler() {
	s.bot.Handle("/monitor", func(c tb.Context) error {
		m := c.Message()
		s.userStats.Store(m.Sender.ID, &userStat{
			user:  m.Sender,
			state: userWaitChooseMonitorCmd,
			lastT: gutils.Clock.GetUTCNow(),
		})

		if _, err := s.bot.Send(m.Sender, gutils.Dedent(`
			Reply number:

				1 - new alert's name  # reply "1 - alert_name"
				2 - list all joint alerts  # reply "2"
				3 - join alert  # reply "3 - alert_name:join_key"
				4 - refresh push_token & join_key  # reply "4 - alert_name"
				5 - quit alert  # reply "5 - alert_name"
				6 - kick user  # reply "6 - alert_name:uid"
			`)); err != nil {
			return errors.Wrap(err, "send msg")
		}

		return nil
	})
}

func (s *Type) chooseMonitor(ctx context.Context, us *userStat, msg *tb.Message) {
	log.Logger.Debug("choose monitor",
		zap.String("user", us.user.Username),
		zap.String("msg", msg.Text))
	defer s.userStats.Delete(us.user.ID)
	var (
		err error
		ans = []string{msg.Text, ""}
	)
	if strings.Contains(msg.Text, " - ") {
		ans = strings.SplitN(msg.Text, " - ", 2)
	}
	if len(ans) < 2 {
		s.PleaseRetry(us.user, msg.Text)
		return
	}

	switch ans[0] {
	case "1": // create new monitor
		if err = s.createNewMonitor(ctx, us, ans[1]); err != nil {
			log.Logger.Warn("createNewMonitor", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "2":
		if err = s.listAllMonitorAlerts(ctx, us); err != nil {
			log.Logger.Warn("listAllMonitorAlerts", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "3":
		if err = s.joinAlertGroup(ctx, us, ans[1]); err != nil {
			log.Logger.Warn("joinAlertGroup", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "4":
		if err = s.refreshAlertTokenAndKey(ctx, us, ans[1]); err != nil {
			log.Logger.Warn("refreshAlertTokenAndKey", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "5":
		if err = s.userQuitAlert(ctx, us, ans[1]); err != nil {
			log.Logger.Warn("userQuitAlert", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "6":
		if err = s.kickUser(ctx, us, ans[1]); err != nil {
			log.Logger.Warn("kickUser", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	default:
		s.PleaseRetry(us.user, msg.Text)
	}
}

func (s *Type) kickUser(ctx context.Context, us *userStat, au string) (err error) {
	if !strings.Contains(au, ":") {
		return errors.Errorf("unknown alert_name:uid format")
	}
	ans := strings.SplitN(strings.TrimSpace(au), ":", 2)
	alertName := ans[0]
	kickUID, err := strconv.Atoi(ans[1])
	if err != nil {
		return errors.Wrap(err, "parse uid to")
	}

	var alertType *model.AlertTypes
	alertType, err = s.monitorDao.IsUserSubAlert(ctx, int(us.user.ID), alertName)
	if err != nil {
		return errors.Wrap(err, "load alert by user uid")
	}

	var kickedUser *model.Users
	kickedUser, err = s.monitorDao.LoadUserByUID(ctx, kickUID)
	if err != nil {
		return errors.Wrap(err, "load user by kicked user uid")
	}

	if err = s.monitorDao.RemoveUAR(ctx, kickedUser.UID, alertName); err != nil {
		return errors.Wrap(err, "remove user_alert_relation")
	}
	log.Logger.Info("remove user_alert_relation",
		zap.String("user_name", kickedUser.Name),
		zap.String("alert_type", alertName),
		zap.String("user", kickedUser.ID.Hex()))

	msg := "<" + us.user.Username + "> kick user:\n"
	msg += "alert_type: " + alertName + "\n"
	msg += "kicked_user: " + kickedUser.Name + " (" + ans[1] + ")\n"

	users, err := s.monitorDao.LoadUsersByAlertType(ctx, alertType)
	if err != nil {
		return errors.Wrap(err, "load users")
	}
	users = append(users, kickedUser)

	errMsg := ""
	for _, user := range users {
		if err = s.SendMsgToUser(user.UID, msg); err != nil {
			errMsg += err.Error()
		}
	}
	if errMsg != "" {
		err = errors.Errorf(errMsg)
	}

	return err
}

func (s *Type) userQuitAlert(ctx context.Context,
	us *userStat, alertName string) (err error) {
	if err = s.monitorDao.RemoveUAR(ctx, int(us.user.ID), alertName); err != nil {
		return errors.Wrap(err, "remove user_alert_relation by uid and alert_name")
	}

	return s.SendMsgToUser(int(us.user.ID), "successed unsubscribe "+alertName)
}

func (s *Type) refreshAlertTokenAndKey(ctx context.Context, us *userStat, alert string) (err error) {
	var alertType *model.AlertTypes
	alertType, err = s.monitorDao.IsUserSubAlert(ctx, int(us.user.ID), alert)
	if err != nil {
		return errors.Wrap(err, "load alert by user uid")
	}
	if err = s.monitorDao.RefreshAlertTokenAndKey(ctx, alertType); err != nil {
		return errors.Wrap(err, "refresh alert token and key")
	}

	msg := "<" + us.user.Username + "> refresh token:\n"
	msg += "alert_type: " + alertType.Name + "\n"
	msg += "push_token: " + alertType.PushToken + "\n"
	msg += "join_key: " + alertType.JoinKey + "\n"

	users, err := s.monitorDao.LoadUsersByAlertType(ctx, alertType)
	if err != nil {
		return errors.Wrap(err, "load users")
	}

	errMsg := ""
	for _, user := range users {
		if err = s.SendMsgToUser(user.UID, msg); err != nil {
			errMsg += err.Error()
		}
	}
	if errMsg != "" {
		err = errors.Errorf(errMsg)
	}

	return err
}

func (s *Type) joinAlertGroup(ctx context.Context, us *userStat, kt string) (err error) {
	if !strings.Contains(kt, ":") {
		return errors.Errorf("unknown format")
	}
	ans := strings.SplitN(strings.TrimSpace(kt), ":", 2)
	alert := ans[0]
	joinKey := ans[1]

	user, err := s.monitorDao.CreateOrGetUser(ctx, us.user)
	if err != nil {
		return err
	}

	uar, err := s.monitorDao.RegisterUserAlertRelation(ctx, user, alert, joinKey)
	if err != nil {
		return err
	}

	return s.SendMsgToUser(int(us.user.ID),
		alert+" (joint at "+uar.CreatedAt.Format(time.RFC3339)+")")
}

func (s *Type) LoadAlertTypesByUser(ctx context.Context,
	u *model.Users) (alerts []*model.AlertTypes, err error) {
	return s.monitorDao.LoadAlertTypesByUser(ctx, u)
}

func (s *Type) LoadAlertTypes(ctx context.Context, cfg *dto.QueryCfg) (alerts []*model.AlertTypes, err error) {
	return s.monitorDao.LoadAlertTypes(ctx, cfg)
}

func (s *Type) LoadUsers(ctx context.Context,
	cfg *dto.QueryCfg) (users []*model.Users, err error) {
	return s.monitorDao.LoadUsers(ctx, cfg)
}

func (s *Type) LoadUsersByAlertType(ctx context.Context,
	a *model.AlertTypes) (users []*model.Users, err error) {
	return s.monitorDao.LoadUsersByAlertType(ctx, a)
}

func (s *Type) ValidateTokenForAlertType(ctx context.Context,
	token, alertType string) (alert *model.AlertTypes, err error) {
	return s.monitorDao.ValidateTokenForAlertType(ctx, token, alertType)
}

func (s *Type) listAllMonitorAlerts(ctx context.Context,
	us *userStat) (err error) {
	u, err := s.monitorDao.LoadUserByUID(ctx, int(us.user.ID))
	if err != nil {
		return err
	}
	alerts, err := s.monitorDao.LoadAlertTypesByUser(ctx, u)
	if err != nil {
		return err
	}

	var msg string
	if len(alerts) == 0 {
		msg = "subscribed no alerts"
	} else {
		msg = ""
		for _, alert := range alerts {
			msg += "--------------------------------\n"
			msg += "alert_type: " + alert.Name + "\n"
			msg += "push_token: " + alert.PushToken + "\n"
			msg += "join_key: " + alert.JoinKey + "\n"
		}
		msg += "--------------------------------"
	}

	return s.SendMsgToUser(u.UID, msg)
}

func (s *Type) createNewMonitor(ctx context.Context, us *userStat, alertName string) (err error) {
	u, err := s.monitorDao.CreateOrGetUser(ctx, us.user)
	if err != nil {
		return errors.Wrap(err, "create user")
	}

	a, err := s.monitorDao.CreateAlertType(ctx, alertName)
	if err != nil {
		return errors.Wrap(err, "create alert_type")
	}

	_, err = s.monitorDao.CreateOrGetUserAlertRelations(ctx, u, a)
	if err != nil {
		return errors.Wrap(err, "create user_alert_relation")
	}

	if _, err = s.bot.Send(us.user, fmt.Sprintf(`
create user & alert_type & user_alert_relations successed!
user: %v
alert_type: %v
join_key: %v
push_token: %v
	`, u.Name,
		a.Name,
		a.JoinKey,
		a.PushToken)); err != nil {
		return errors.Wrap(err, "send msg")
	}

	return nil
}
