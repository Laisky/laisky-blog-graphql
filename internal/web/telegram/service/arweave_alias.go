package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v5"
	gutils "github.com/Laisky/go-utils/v4"
	"github.com/Laisky/zap"
	"github.com/golang-jwt/jwt/v4"
	tb "gopkg.in/telebot.v3"

	"github.com/Laisky/laisky-blog-graphql/library/auth"
	ijwt "github.com/Laisky/laisky-blog-graphql/library/jwt"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

var httpcli *http.Client

func init() {
	var err error
	httpcli, err = gutils.NewHTTPClient(
		gutils.WithHTTPClientTimeout(time.Second * 30),
	)
	if err != nil {
		log.Logger.Panic("new httpcli", zap.Error(err))
	}
}

var regexpAlias = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{3,64}$`)

func (s *Telegram) registerArweaveAliasHandler() {
	s.bot.Handle("/arweave_alias", func(c tb.Context) error {
		m := c.Message()
		s.userStats.Store(m.Sender.ID, &userStat{
			user:  m.Sender,
			state: userWaitArweaveAliasCmd,
			lastT: gutils.Clock.GetUTCNow(),
		})

		if _, err := s.bot.Send(m.Sender, gutils.Dedent(`
			Reply number:

				1 - create alias  # reply "1 - <ALIAS_NAME> arweave_file_id"
				2 - update alias  # reply "2 - <ALIAS_NAME> arweave_file_id"
				3 - get alias     # reply "3 - <ALIAS_NAME>"

			<ALIAS_NAME> must match ^[a-zA-Z0-9_\-\.]{3,64}$

			For more info, check this doc: https://ario.laisky.com/alias/doc

			Check all DNS records at this site(up to 1000 records, refresh every 10 minutes): https://ario.laisky.com/dns
			`)); err != nil {
			return errors.Wrap(err, "send msg")
		}

		return nil
	})
}

func (s *Telegram) arweaveAliasHandler(ctx context.Context, us *userStat, msg *tb.Message) {
	logger := gmw.GetLogger(ctx).With(
		zap.String("user", us.user.Username),
		zap.String("msg", msg.Text),
	)
	logger.Debug("choose alias cmd")
	// defer s.userStats.Delete(us.user.ID)

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
	case "1": // create arweave alias
		if err = s.arweaveCreateAlias(ctx, us, ans[1]); err != nil {
			logger.Warn("arweaveCreateAlias", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "2":
		if err = s.arweaveUpdateAlias(ctx, us, ans[1]); err != nil {
			logger.Warn("arweaveUpdateAlias", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	case "3":
		if err = s.arweaveGetAlias(ctx, us, ans[1]); err != nil {
			logger.Warn("arweaveGetAlias", zap.Error(err))
			if _, err = s.bot.Send(us.user, "[Error] "+err.Error()); err != nil {
				logger.Error("send msg by telegram", zap.Error(err))
			}
		}
	default:
		s.PleaseRetry(us.user, msg.Text)
	}
}

func (s *Telegram) arweaveCreateAlias(ctx context.Context, us *userStat, msg string) error {
	logger := gmw.GetLogger(ctx)

	msgParts := strings.Split(msg, " ")
	if len(msgParts) != 2 {
		return errors.New("msg format should be `1 - alias arweave_file_id`")
	}

	alias := msgParts[0]
	fileid := msgParts[1]

	if !regexpAlias.MatchString(alias) {
		return errors.New(`alias should be ^[a-zA-Z0-9_\-\.]{3,64}$`)
	}

	userclaim := ijwt.NewUserClaims()
	userclaim.Subject = strconv.Itoa(int(us.user.ID))
	userclaim.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute * 5))
	userToken, err := auth.Instance.Sign(userclaim)
	if err != nil {
		return errors.Wrap(err, "sign user token")
	}

	body, err := json.Marshal(map[string]any{
		"name":    alias,
		"file_id": fileid,
		"owner": map[string]int{
			"telegram_uid": int(us.user.ID),
		},
	})
	if err != nil {
		return errors.Wrap(err, "marshal body")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://ario.laisky.com/dns/", bytes.NewReader(body),
	)
	if err != nil {
		return errors.Wrap(err, "new request")
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", userToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpcli.Do(req)
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer gutils.CloseWithLog(resp.Body, logger)

	if resp.StatusCode != http.StatusOK {
		cnt, err := io.ReadAll(resp.Body)
		if err != nil {
			return errors.Wrapf(err, "[%d]read error body", resp.StatusCode)
		}

		return errors.Errorf("request failed: [%d]%s", resp.StatusCode, string(cnt))
	}

	if _, err = s.bot.Send(us.user, "https://ario.laisky.com/alias/"+alias); err != nil {
		return errors.Wrap(err, "send msg")
	}

	return nil
}

func (s *Telegram) arweaveUpdateAlias(ctx context.Context, us *userStat, msg string) error {
	logger := gmw.GetLogger(ctx)

	msgParts := strings.Split(msg, " ")
	if len(msgParts) != 2 {
		return errors.New("msg format should be `2 - alias arweave_file_id`")
	}

	alias := msgParts[0]
	fileid := msgParts[1]

	if !regexpAlias.MatchString(alias) {
		return errors.New("alias should be [a-zA-Z0-9_-]")
	}

	userclaim := ijwt.NewUserClaims()
	userclaim.Subject = strconv.Itoa(int(us.user.ID))
	userclaim.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute * 5))
	userToken, err := auth.Instance.Sign(userclaim)
	if err != nil {
		return errors.Wrap(err, "sign user token")
	}

	body, err := json.Marshal(map[string]any{
		"name":    alias,
		"file_id": fileid,
		"owner": map[string]int{
			"telegram_uid": int(us.user.ID),
		},
	})
	if err != nil {
		return errors.Wrap(err, "marshal body")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		"https://ario.laisky.com/dns/", bytes.NewReader(body),
	)
	if err != nil {
		return errors.Wrap(err, "new request")
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", userToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpcli.Do(req)
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer gutils.CloseWithLog(resp.Body, logger)

	if resp.StatusCode != http.StatusOK {
		cnt, err := io.ReadAll(resp.Body)
		if err != nil {
			return errors.Wrapf(err, "[%d]read error body", resp.StatusCode)
		}

		return errors.Errorf("request failed: [%d]%s", resp.StatusCode, string(cnt))
	}

	if _, err = s.bot.Send(us.user, "https://ario.laisky.com/alias/"+alias); err != nil {
		return errors.Wrap(err, "send msg")
	}

	return nil
}

func (s *Telegram) arweaveGetAlias(ctx context.Context, us *userStat, alias string) error {
	logger := gmw.GetLogger(ctx)

	if !regexpAlias.MatchString(alias) {
		return errors.New("alias should be [a-zA-Z0-9_-]")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://ario.laisky.com/dns/"+alias, nil,
	)
	if err != nil {
		return errors.Wrap(err, "new request")
	}

	resp, err := httpcli.Do(req)
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer gutils.CloseWithLog(resp.Body, logger)

	if resp.StatusCode != http.StatusOK {
		cnt, err := io.ReadAll(resp.Body)
		if err != nil {
			return errors.Wrapf(err, "[%d]read error body", resp.StatusCode)
		}

		return errors.Errorf("request failed: [%d]%s", resp.StatusCode, string(cnt))
	}

	cnt, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "read body")
	}

	if _, err = s.bot.Send(us.user, string(cnt)); err != nil {
		return errors.Wrap(err, "send msg")
	}

	return nil
}
