package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	tb "gopkg.in/tucnak/telebot.v2"
)

func (b *Telegram) monitorHandler() {
	b.bot.Handle("/monitor", func(c *tb.Message) {
		b.userStats.Store(c.Sender.ID, &userStat{
			user:  c.Sender,
			state: userWaitChooseMonitorCmd,
			lastT: utils.Clock.GetUTCNow(),
		})

		if _, err := b.bot.Send(c.Sender, `
Reply number:

	1 - new alert's name  # reply "1 - alert_name"
	2 - list all joint alerts  # reply "2"
	3 - join alert  # reply "3 - alert_name:join_key"
	4 - refresh push_token & join_key  # reply "4 - alert_name"
	5 - quit alert  # reply "5 - alert_name"
	6 - kick user  # reply "6 - alert_name:uid"
`); err != nil {
			utils.Logger.Error("reply msg", zap.Error(err))
		}
	})
}

func (b *Telegram) chooseMonitor(us *userStat, msg *tb.Message) {
	utils.Logger.Debug("choose monitor",
		zap.String("user", us.user.Username),
		zap.String("msg", msg.Text))
	defer b.userStats.Delete(us.user.ID)
	var (
		err error
		ans = []string{msg.Text, ""}
	)
	if strings.Contains(msg.Text, " - ") {
		ans = strings.SplitN(msg.Text, " - ", 2)
	}
	if len(ans) < 2 {
		b.PleaseRetry(us.user, msg.Text)
		return
	}

	switch ans[0] {
	case "1": // create new monitor
		if err = b.createNewMonitor(us, ans[1]); err != nil {
			utils.Logger.Warn("createNewMonitor", zap.Error(err))
			b.bot.Send(us.user, "[Error] "+err.Error())
		}
	case "2":
		if err = b.listAllMonitorAlerts(us); err != nil {
			utils.Logger.Warn("listAllMonitorAlerts", zap.Error(err))
			b.bot.Send(us.user, "[Error] "+err.Error())
		}
	case "3":
		if err = b.joinAlertGroup(us, ans[1]); err != nil {
			utils.Logger.Warn("joinAlertGroup", zap.Error(err))
			b.bot.Send(us.user, "[Error] "+err.Error())
		}
	case "4":
		if err = b.refreshAlertTokenAndKey(us, ans[1]); err != nil {
			utils.Logger.Warn("refreshAlertTokenAndKey", zap.Error(err))
			b.bot.Send(us.user, "[Error] "+err.Error())
		}
	case "5":
		if err = b.userQuitAlert(us, ans[1]); err != nil {
			utils.Logger.Warn("userQuitAlert", zap.Error(err))
			b.bot.Send(us.user, "[Error] "+err.Error())
		}
	case "6":
		if err = b.kickUser(us, ans[1]); err != nil {
			utils.Logger.Warn("kickUser", zap.Error(err))
			b.bot.Send(us.user, "[Error] "+err.Error())
		}
	default:
		b.PleaseRetry(us.user, msg.Text)
	}
}

func (b *Telegram) kickUser(us *userStat, au string) (err error) {
	if !strings.Contains(au, ":") {
		return fmt.Errorf("unknown alert_name:uid format")
	}
	ans := strings.SplitN(strings.TrimSpace(au), ":", 2)
	alertName := ans[0]
	kickUID, err := strconv.ParseInt(ans[1], 10, 64)
	if err != nil {
		return errors.Wrap(err, "parse uid to")
	}

	var alertType *AlertTypes
	alertType, err = b.db.IsUserSubAlert(us.user.ID, alertName)
	if err != nil {
		return errors.Wrap(err, "load alert by user uid")
	}

	var kickedUser *Users
	kickedUser, err = b.db.LoadUserByUID(int(kickUID))
	if err != nil {
		return errors.Wrap(err, "load user by kicked user uid")
	}

	if err = b.db.RemoveUAR(kickedUser.UID, alertName); err != nil {
		return errors.Wrap(err, "remove user_alert_relation")
	}
	utils.Logger.Info("remove user_alert_relation",
		zap.String("user_name", kickedUser.Name),
		zap.String("alert_type", alertName),
		zap.String("user", kickedUser.ID.Hex()))

	msg := "<" + us.user.Username + "> kick user:\n"
	msg += "alert_type: " + alertName + "\n"
	msg += "kicked_user: " + kickedUser.Name + " (" + ans[1] + ")\n"

	users, err := b.db.LoadUsersByAlertType(alertType)
	if err != nil {
		return errors.Wrap(err, "load users")
	}
	users = append(users, kickedUser)

	errMsg := ""
	for _, user := range users {
		if err = b.SendMsgToUser(user.UID, msg); err != nil {
			errMsg += err.Error()
		}
	}
	if errMsg != "" {
		err = fmt.Errorf(errMsg)
	}

	return err
}

func (b *Telegram) userQuitAlert(us *userStat, alertName string) (err error) {
	if err = b.db.RemoveUAR(us.user.ID, alertName); err != nil {
		return errors.Wrap(err, "remove user_alert_relation by uid and alert_name")
	}

	return b.SendMsgToUser(us.user.ID, "successed unsubscribe "+alertName)
}

func (b *Telegram) refreshAlertTokenAndKey(us *userStat, alert string) (err error) {
	var alertType *AlertTypes
	alertType, err = b.db.IsUserSubAlert(us.user.ID, alert)
	if err != nil {
		return errors.Wrap(err, "load alert by user uid")
	}
	if err = b.db.RefreshAlertTokenAndKey(alertType); err != nil {
		return errors.Wrap(err, "refresh alert token and key")
	}

	msg := "<" + us.user.Username + "> refresh token:\n"
	msg += "alert_type: " + alertType.Name + "\n"
	msg += "push_token: " + alertType.PushToken + "\n"
	msg += "join_key: " + alertType.JoinKey + "\n"

	users, err := b.db.LoadUsersByAlertType(alertType)
	if err != nil {
		return errors.Wrap(err, "load users")
	}

	errMsg := ""
	for _, user := range users {
		if err = b.SendMsgToUser(user.UID, msg); err != nil {
			errMsg += err.Error()
		}
	}
	if errMsg != "" {
		err = fmt.Errorf(errMsg)
	}

	return err
}

func (b *Telegram) joinAlertGroup(us *userStat, kt string) (err error) {
	if !strings.Contains(kt, ":") {
		return fmt.Errorf("unknown format")
	}
	ans := strings.SplitN(strings.TrimSpace(kt), ":", 2)
	alert := ans[0]
	joinKey := ans[1]

	uar, err := b.db.RegisterUserAlertRelation(us.user.ID, alert, joinKey)
	if err != nil {
		return err
	}

	return b.SendMsgToUser(us.user.ID, alert+" (joint at "+uar.CreatedAt.Format(time.RFC3339)+")")
}

func (b *Telegram) listAllMonitorAlerts(us *userStat) (err error) {
	u, err := b.db.LoadUserByUID(us.user.ID)
	if err != nil {
		return err
	}
	alerts, err := b.db.LoadAlertTypesByUser(u)
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

	return b.SendMsgToUser(u.UID, msg)
}

func (b *Telegram) createNewMonitor(us *userStat, alertName string) (err error) {
	u, err := b.db.CreateOrGetUser(us.user)
	if err != nil {
		return errors.Wrap(err, "create user")
	}

	a, err := b.db.CreateAlertType(alertName)
	if err != nil {
		return errors.Wrap(err, "create alert_type")
	}

	_, err = b.db.CreateOrGetUserAlertRelations(u, a)
	if err != nil {
		return errors.Wrap(err, "create user_alert_relation")
	}

	if _, err = b.bot.Send(us.user, fmt.Sprintf(`
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
