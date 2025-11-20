package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
	"github.com/google/uuid"
	tb "gopkg.in/telebot.v3"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

func (s *Telegram) SetAskUserService(svc *askuser.Service) {
	s.askUserService = svc
	svc.RegisterNotifier(s)
}

func (s *Telegram) OnNewRequest(req *askuser.Request) {
	ctx := context.Background()
	uid, err := s.lookupTelegramUID(ctx, req.APIKeyHash)
	if err != nil {
		// It's normal that not all requests have a linked telegram user
		return
	}

	msgText := fmt.Sprintf("❓ *New Question*\n\n%s", escapeMsg(req.Question))
	msg, err := s.bot.Send(&tb.User{ID: int64(uid)}, msgText, &tb.SendOptions{
		ParseMode: tb.ModeMarkdown,
	})
	if err != nil {
		log.Logger.Error("failed to send ask_user question to telegram", zap.Error(err), zap.Int("uid", uid))
		return
	}

	s.askUserRequests.Store(msg.ID, req.ID)
	s.trackAskUserSession(int64(uid), msg.ID, req.ID)
	log.Logger.Debug("tracked ask_user session", zap.Int("uid", uid), zap.Int("prompt_msg_id", msg.ID), zap.String("request_id", req.ID.String()))
}

func (s *Telegram) OnRequestCancelled(req *askuser.Request) {
	ctx := context.Background()
	uid, err := s.lookupTelegramUID(ctx, req.APIKeyHash)
	if err != nil {
		return
	}

	msgText := fmt.Sprintf("❌ *Question Cancelled*\n\nThe question has been cancelled or expired: %s", escapeMsg(req.Question))
	if _, err := s.bot.Send(&tb.User{ID: int64(uid)}, msgText, &tb.SendOptions{
		ParseMode: tb.ModeMarkdown,
	}); err != nil {
		log.Logger.Error("failed to send ask_user cancellation to telegram", zap.Error(err), zap.Int("uid", uid))
	}

	s.clearAskUserSession(int64(uid), 0, req.ID)
	log.Logger.Debug("cleared ask_user session due to cancellation", zap.Int("uid", uid), zap.String("request_id", req.ID.String()))
}

func (s *Telegram) registerAskUserHandler(ctx context.Context) {
	logger := gmw.GetLogger(ctx)
	s.bot.Handle("/askuser", func(c tb.Context) error {
		payloadProvided := strings.TrimSpace(c.Message().Payload) != ""
		logger.Debug("ask_user link command", zap.Int64("uid", c.Sender().ID), zap.Bool("payload_provided", payloadProvided))
		us := &userStat{
			user:  c.Sender(),
			state: userWaitAskUserToken,
			lastT: gutils.Clock.GetUTCNow(),
			data:  map[string]string{},
		}
		s.userStats.Store(c.Sender().ID, us)

		prompt := buildAskUserIntroPrompt(payloadProvided)
		logger.Debug("ask_user prompt prepared", zap.Int64("uid", c.Sender().ID), zap.Int("prompt_len", len(prompt)))

		if _, err := s.bot.Send(c.Sender(), prompt, &tb.SendOptions{
			ParseMode:             tb.ModeMarkdown,
			DisableWebPagePreview: true,
		}); err != nil {
			logger.Error("send ask_user prompt", zap.Error(err), zap.Int("prompt_len", len(prompt)))
			return err
		}
		return nil
	})
}

func (s *Telegram) askUserTokenHandler(ctx context.Context, us *userStat, msg *tb.Message) {
	logger := gmw.GetLogger(ctx).With(zap.Int64("uid", msg.Sender.ID))
	input := strings.TrimSpace(msg.Text)
	if input == "" {
		if _, err := s.bot.Send(us.user, "Please reply with your API key as plain text or send `cancel` to stop.", &tb.SendOptions{
			ParseMode:             tb.ModeMarkdown,
			DisableWebPagePreview: true,
		}); err != nil {
			logger.Error("send ask_user token prompt", zap.Error(err))
		}
		return
	}

	if strings.EqualFold(input, "cancel") {
		s.userStats.Delete(us.user.ID)
		if _, err := s.bot.Send(us.user, "Linking cancelled. You can run /askuser again anytime.", nil); err != nil {
			logger.Error("send ask_user cancel ack", zap.Error(err))
		}
		return
	}

	validatedKey, err := s.validateOneAPIToken(ctx, input)
	if err != nil {
		logger.Warn("validate oneapi key", zap.Error(err), zap.String("token_mask", maskToken(input)))
		if _, sendErr := s.bot.Send(us.user, "Invalid OneAPI API key. Please double-check and try again.", &tb.SendOptions{
			ParseMode:             tb.ModeMarkdown,
			DisableWebPagePreview: true,
		}); sendErr != nil {
			logger.Error("send ask_user invalid key msg", zap.Error(sendErr))
		}
		return
	}

	hashed := sha256.Sum256([]byte(validatedKey))
	tokenHash := hex.EncodeToString(hashed[:])
	mask := maskToken(validatedKey)
	if us.data == nil {
		us.data = make(map[string]string)
	}
	us.data["token_hash"] = tokenHash
	us.data["token_mask"] = mask
	us.state = userWaitAskUserConfirm
	us.lastT = gutils.Clock.GetUTCNow()
	s.userStats.Store(us.user.ID, us)

	prompt := buildAskUserConfirmPrompt(mask)
	if _, err := s.bot.Send(us.user, prompt, &tb.SendOptions{
		ParseMode:             tb.ModeMarkdown,
		DisableWebPagePreview: true,
	}); err != nil {
		logger.Error("send ask_user confirm prompt", zap.Error(err))
	}
}

func (s *Telegram) askUserTokenConfirmHandler(ctx context.Context, us *userStat, msg *tb.Message) {
	logger := gmw.GetLogger(ctx).With(zap.Int64("uid", msg.Sender.ID))
	input := strings.TrimSpace(msg.Text)
	if input == "" {
		if _, err := s.bot.Send(us.user, "Please reply `yes` to confirm or `cancel` to abort.", &tb.SendOptions{
			ParseMode:             tb.ModeMarkdown,
			DisableWebPagePreview: true,
		}); err != nil {
			logger.Error("send ask_user confirm reminder", zap.Error(err))
		}
		return
	}

	if strings.EqualFold(input, "cancel") {
		s.userStats.Delete(us.user.ID)
		if _, err := s.bot.Send(us.user, "Linking cancelled. You can run /askuser again anytime.", nil); err != nil {
			logger.Error("send ask_user cancel ack", zap.Error(err))
		}
		return
	}

	if !(strings.EqualFold(input, "yes") || strings.EqualFold(input, "y") || strings.EqualFold(input, "confirm")) {
		if _, err := s.bot.Send(us.user, "Reply `yes` to confirm linking or `cancel` to abort.", &tb.SendOptions{
			ParseMode:             tb.ModeMarkdown,
			DisableWebPagePreview: true,
		}); err != nil {
			logger.Error("send ask_user confirm reminder", zap.Error(err))
		}
		return
	}

	var tokenHash string
	if us.data != nil {
		tokenHash = us.data["token_hash"]
	}
	if tokenHash == "" {
		logger.Warn("ask_user token hash missing during confirm")
		s.userStats.Delete(us.user.ID)
		if _, err := s.bot.Send(us.user, "Session expired. Please run /askuser again.", nil); err != nil {
			logger.Error("send ask_user session expired", zap.Error(err))
		}
		return
	}

	if err := s.registerTelegramUID(ctx, int(us.user.ID), tokenHash); err != nil {
		logger.Error("register ask_user token", zap.Error(err))
		if _, err := s.bot.Send(us.user, "Failed to register token. Please try again later.", nil); err != nil {
			logger.Error("send ask_user register error", zap.Error(err))
		}
		return
	}

	mask := ""
	if us.data != nil {
		mask = us.data["token_mask"]
	}
	s.userStats.Delete(us.user.ID)
	message := "✅ Successfully linked your OneAPI API key. You'll now receive ask\\_user questions here."
	if mask != "" {
		message = fmt.Sprintf("✅ Successfully linked API key `%s`. You'll now receive ask\\_user questions here.", mask)
	}
	if _, err := s.bot.Send(us.user, message, &tb.SendOptions{
		ParseMode:             tb.ModeMarkdown,
		DisableWebPagePreview: true,
	}); err != nil {
		logger.Error("send ask_user success", zap.Error(err))
	}
}

func maskToken(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return "***"
	}
	if len(trimmed) <= 4 {
		return fmt.Sprintf("***%s", trimmed)
	}
	return fmt.Sprintf("***%s", trimmed[len(trimmed)-4:])
}

func buildAskUserIntroPrompt(payloadProvided bool) string {
	base := "Step 1/2: Reply with the OneAPI API key you want to link to MCP ask\\_user.\n" +
		"Send `cancel` at any time to stop."
	if payloadProvided {
		base += "\n\nFor safety, please send the key as a normal message instead of embedding it in the command."
	}
	return base
}

func buildAskUserConfirmPrompt(mask string) string {
	safeMask := mask
	if safeMask == "" {
		safeMask = "***"
	}
	return fmt.Sprintf("Step 2/2: We'll link API key `%s`.\nReply `yes` to confirm or `cancel` to abort.\nWe only store a hashed copy of your key.", safeMask)
}

func (s *Telegram) registerTelegramUID(ctx context.Context, uid int, tokenHash string) error {
	if s.askUserTokenDao == nil {
		return errors.New("ask_user token dao not configured")
	}
	return s.askUserTokenDao.RegisterAskUserToken(ctx, uid, tokenHash)
}

func (s *Telegram) lookupTelegramUID(ctx context.Context, tokenHash string) (int, error) {
	if s.askUserTokenDao == nil {
		return 0, errors.New("ask_user token dao not configured")
	}
	return s.askUserTokenDao.GetTelegramUIDByTokenHash(ctx, tokenHash)
}

func (s *Telegram) handleAskUserAnswer(ctx context.Context, c tb.Context, reqID uuid.UUID, promptMsgID int) error {
	if s.askUserService == nil {
		return c.Send("AskUser service is not available.")
	}

	req, err := s.askUserService.GetRequest(ctx, reqID)
	if err != nil {
		log.Logger.Error("failed to get ask_user request", zap.Error(err))
		return c.Send("Failed to retrieve the question. It might have expired.")
	}

	// Verify sender identity
	uid, err := s.lookupTelegramUID(ctx, req.APIKeyHash)
	if err != nil || uid != int(c.Sender().ID) {
		return c.Send("⛔ You are not authorized to answer this question.")
	}

	if req.Status != askuser.StatusPending {
		return c.Send(fmt.Sprintf("⚠️ This question is no longer pending (Status: %s).", req.Status))
	}

	// Construct a fake auth context with the correct hash
	auth := &askuser.AuthorizationContext{
		APIKeyHash:   req.APIKeyHash,
		UserIdentity: fmt.Sprintf("telegram:%d", c.Sender().ID),
	}

	answer := c.Message().Text
	if _, err := s.askUserService.AnswerRequest(ctx, auth, reqID, answer); err != nil {
		log.Logger.Error("failed to answer ask_user request", zap.Error(err))
		return c.Send("Failed to submit your answer. Please try again.")
	}

	s.clearAskUserSession(c.Sender().ID, promptMsgID, reqID)
	return c.Send("✅ Answer submitted!")
}

// escapeMsg escapes special characters in a message to prevent Telegram from interpreting them as formatting
func escapeMsg(msg string) string {
	// Escape special characters that Telegram interprets as formatting
	// Replace backticks with single quotes to avoid code block formatting issues
	msg = strings.ReplaceAll(msg, "`", "'")

	// Escape other special Telegram formatting characters
	msg = strings.ReplaceAll(msg, "_", "\\_")
	msg = strings.ReplaceAll(msg, "*", "\\*")
	msg = strings.ReplaceAll(msg, "[", "\\[")
	msg = strings.ReplaceAll(msg, "]", "\\]")
	msg = strings.ReplaceAll(msg, "(", "\\(")
	msg = strings.ReplaceAll(msg, ")", "\\)")
	msg = strings.ReplaceAll(msg, "~", "\\~")
	msg = strings.ReplaceAll(msg, ">", "\\>")
	msg = strings.ReplaceAll(msg, "#", "\\#")
	msg = strings.ReplaceAll(msg, "+", "\\+")
	msg = strings.ReplaceAll(msg, "-", "\\-")
	msg = strings.ReplaceAll(msg, "=", "\\=")
	msg = strings.ReplaceAll(msg, "|", "\\|")
	msg = strings.ReplaceAll(msg, "{", "\\{")
	msg = strings.ReplaceAll(msg, "}", "\\}")

	return msg
}
