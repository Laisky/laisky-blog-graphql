package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"laisky-blog-graphql/library/log"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	tb "gopkg.in/tucnak/telebot.v2"
)

func (s *Service) monitorHandler() {
	s.bot.Handle("/monitor", func(c *tb.Message) {
		s.userStats.Store(c.Sender.ID, &userStat{
			user:  c.Sender,
			state: userWaitChooseMonitorCmd,
			lastT: utils.Clock.GetUTCNow(),
		})

		if _, err := s.bot.Send(c.Sender, `
Reply number:

	1 - new alert's name  # reply "1 - alert_name"
	2 - list all joint alerts  # reply "2"
	3 - join alert  # reply "3 - alert_name:join_key"
	4 - refresh push_token & join_key  # reply "4 - alert_name"
	5 - quit alert  # reply "5 - alert_name"
	6 - kick user  # reply "6 - alert_name:uid"
`); err != nil {
			log.Logger.Error("reply msg", zap.Error(err))
		}
	})
}

func (s *Service) chooseMonitor(us *userStat, msg *tb.Message) {
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
		if err = s.createNewMonitor(us, ans[1]); err != nil {
			log.Logger.Warn("createNewMonitor", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "2":
		if err = s.listAllMonitorAlerts(us); err != nil {
			log.Logger.Warn("listAllMonitorAlerts", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "3":
		if err = s.joinAlertGroup(us, ans[1]); err != nil {
			log.Logger.Warn("joinAlertGroup", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "4":
		if err = s.refreshAlertTokenAndKey(us, ans[1]); err != nil {
			log.Logger.Warn("refreshAlertTokenAndKey", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "5":
		if err = s.userQuitAlert(us, ans[1]); err != nil {
			log.Logger.Warn("userQuitAlert", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "6":
		if err = s.kickUser(us, ans[1]); err != nil {
			log.Logger.Warn("kickUser", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				log.Logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	default:
		s.PleaseRetry(us.user, msg.Text)
	}
}

func (s *Service) kickUser(us *userStat, au string) (err error) {
	if !strings.Contains(au, ":") {
		return fmt.Errorf("unknown alert_name:uid format")
	}
	ans := strings.SplitN(strings.TrimSpace(au), ":", 2)
	alertName := ans[0]
	kickUID, err := strconv.Atoi(ans[1])
	if err != nil {
		return errors.Wrap(err, "parse uid to")
	}

	var alertType *AlertTypes
	alertType, err = s.IsUserSubAlert(us.user.ID, alertName)
	if err != nil {
		return errors.Wrap(err, "load alert by user uid")
	}

	var kickedUser *Users
	kickedUser, err = s.LoadUserByUID(kickUID)
	if err != nil {
		return errors.Wrap(err, "load user by kicked user uid")
	}

	if err = s.RemoveUAR(kickedUser.UID, alertName); err != nil {
		return errors.Wrap(err, "remove user_alert_relation")
	}
	log.Logger.Info("remove user_alert_relation",
		zap.String("user_name", kickedUser.Name),
		zap.String("alert_type", alertName),
		zap.String("user", kickedUser.ID.Hex()))

	msg := "<" + us.user.Username + "> kick user:\n"
	msg += "alert_type: " + alertName + "\n"
	msg += "kicked_user: " + kickedUser.Name + " (" + ans[1] + ")\n"

	users, err := s.LoadUsersByAlertType(alertType)
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
		err = fmt.Errorf(errMsg)
	}

	return err
}

func (s *Service) userQuitAlert(us *userStat, alertName string) (err error) {
	if err = s.RemoveUAR(us.user.ID, alertName); err != nil {
		return errors.Wrap(err, "remove user_alert_relation by uid and alert_name")
	}

	return s.SendMsgToUser(us.user.ID, "successed unsubscribe "+alertName)
}

func (s *Service) refreshAlertTokenAndKey(us *userStat, alert string) (err error) {
	var alertType *AlertTypes
	alertType, err = s.IsUserSubAlert(us.user.ID, alert)
	if err != nil {
		return errors.Wrap(err, "load alert by user uid")
	}
	if err = s.RefreshAlertTokenAndKey(alertType); err != nil {
		return errors.Wrap(err, "refresh alert token and key")
	}

	msg := "<" + us.user.Username + "> refresh token:\n"
	msg += "alert_type: " + alertType.Name + "\n"
	msg += "push_token: " + alertType.PushToken + "\n"
	msg += "join_key: " + alertType.JoinKey + "\n"

	users, err := s.LoadUsersByAlertType(alertType)
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
		err = fmt.Errorf(errMsg)
	}

	return err
}

func (s *Service) joinAlertGroup(us *userStat, kt string) (err error) {
	if !strings.Contains(kt, ":") {
		return fmt.Errorf("unknown format")
	}
	ans := strings.SplitN(strings.TrimSpace(kt), ":", 2)
	alert := ans[0]
	joinKey := ans[1]

	user, err := s.CreateOrGetUser(us.user)
	if err != nil {
		return err
	}

	uar, err := s.RegisterUserAlertRelation(user, alert, joinKey)
	if err != nil {
		return err
	}

	return s.SendMsgToUser(us.user.ID, alert+" (joint at "+uar.CreatedAt.Format(time.RFC3339)+")")
}

func (s *Service) listAllMonitorAlerts(us *userStat) (err error) {
	u, err := s.LoadUserByUID(us.user.ID)
	if err != nil {
		return err
	}
	alerts, err := s.LoadAlertTypesByUser(u)
	if err != nil {
		return err
	}

	msg := ""
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

func (s *Service) createNewMonitor(us *userStat, alertName string) (err error) {
	u, err := s.CreateOrGetUser(us.user)
	if err != nil {
		return errors.Wrap(err, "create user")
	}

	a, err := s.CreateAlertType(alertName)
	if err != nil {
		return errors.Wrap(err, "create alert_type")
	}

	_, err = s.CreateOrGetUserAlertRelations(u, a)
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
