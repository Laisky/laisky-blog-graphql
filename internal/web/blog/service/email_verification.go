package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/go-utils/v6/email"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	blogoneapi "github.com/Laisky/laisky-blog-graphql/internal/web/blog/oneapi"
)

const (
	// EmailVerificationCodeTTL is the validity window for SSO email verification codes.
	EmailVerificationCodeTTL    = 30 * time.Minute
	emailVerificationCodeDigits = 6
	// defaultSMTPTLSPort is the implicit-TLS SMTP submission port used when
	// settings.web.smtp.port is unset. Port 465 enables SSL/TLS on connect.
	defaultSMTPTLSPort = 465
)

// smtpDialerFactory overrides the SMTP dialer, used only by tests. When nil the
// default gomail dialer is used, which enables implicit TLS on port 465.
var smtpDialerFactory func(host string, port int, username, passwd string) email.Sender

// RequestEmailVerificationCode creates and emails a one-time verification code.
// It accepts account and purpose, returning nil after the SMTP relay accepts the message.
func (s *Blog) RequestEmailVerificationCode(ctx context.Context, account string, purpose string) error {
	account, purpose, err := sanitizeEmailVerificationRequest(account, purpose)
	if err != nil {
		return errors.Wrap(err, "sanitize email verification request")
	}

	shouldSend, err := s.shouldSendEmailVerificationCode(ctx, account, purpose)
	if err != nil {
		return errors.Wrap(err, "check email verification eligibility")
	}
	if !shouldSend {
		return nil
	}

	code, err := generateEmailVerificationCode()
	if err != nil {
		return errors.Wrap(err, "generate email verification code")
	}
	now := gutils.Clock.GetUTCNow()
	if s.oneapi != nil {
		challenge := blogoneapi.SSOEmailVerificationCode{
			ID:        gutils.UUID7(),
			Account:   account,
			Purpose:   purpose,
			CodeHash:  hashEmailVerificationCode(account, purpose, code),
			CreatedAt: now,
			ExpiresAt: now.Add(EmailVerificationCodeTTL),
		}
		if err = s.oneapi.ReplaceEmailCode(ctx, challenge); err != nil {
			return errors.Wrap(err, "replace oneapi email verification code")
		}
		if err = s.sendSMTPVerificationCode(account, code, purpose); err != nil {
			if cleanupErr := s.oneapi.DeleteEmailCode(ctx, challenge.ID); cleanupErr != nil {
				s.logger.Warn("delete undelivered oneapi email code", zap.Error(cleanupErr))
			}
			return errors.Wrap(err, "send verification email")
		}
		return nil
	}
	challenge := &model.EmailVerificationCode{
		ID:        primitive.NewObjectID(),
		Account:   account,
		Purpose:   purpose,
		CodeHash:  hashEmailVerificationCode(account, purpose, code),
		CreatedAt: now,
		ExpiresAt: now.Add(EmailVerificationCodeTTL),
	}

	col := s.dao.GetEmailVerificationCodesCol()
	if _, err = col.DeleteMany(ctx, bson.M{"account": account, "purpose": purpose}); err != nil {
		return errors.Wrap(err, "delete previous email verification codes")
	}
	if _, err = col.InsertOne(ctx, challenge); err != nil {
		return errors.Wrap(err, "insert email verification code")
	}
	if err = s.sendSMTPVerificationCode(account, code, purpose); err != nil {
		return errors.Wrap(err, "send verification email")
	}

	return nil
}

// ConsumeEmailVerificationCode verifies and removes a one-time email code.
// It accepts account, purpose, and code, returning nil when the code is valid.
func (s *Blog) ConsumeEmailVerificationCode(ctx context.Context, account string, purpose string, code string) error {
	account, purpose, err := sanitizeEmailVerificationRequest(account, purpose)
	if err != nil {
		return errors.Wrap(err, "sanitize email verification request")
	}
	code, err = sanitizeEmailVerificationCode(code)
	if err != nil {
		return errors.Wrap(err, "sanitize email verification code")
	}
	if s.oneapi != nil {
		challenge, findErr := s.oneapi.FindValidEmailCode(ctx, account, purpose, gutils.Clock.GetUTCNow())
		if findErr != nil {
			return errors.New("invalid email verification code")
		}
		expectedHash := hashEmailVerificationCode(account, purpose, code)
		if !secureCompareString(challenge.CodeHash, expectedHash) {
			return errors.New("invalid email verification code")
		}
		if consumeErr := s.oneapi.ConsumeEmailCode(ctx, challenge.ID, expectedHash); consumeErr != nil {
			return errors.New("invalid email verification code")
		}
		return nil
	}

	col := s.dao.GetEmailVerificationCodesCol()
	challenge := new(model.EmailVerificationCode)
	if err = col.FindOne(ctx, bson.M{
		"account":    account,
		"purpose":    purpose,
		"expires_at": bson.M{"$gt": gutils.Clock.GetUTCNow()},
	}).Decode(challenge); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return errors.New("invalid email verification code")
		}
		return errors.Wrap(err, "find email verification code")
	}

	expectedHash := hashEmailVerificationCode(account, purpose, code)
	if !secureCompareString(challenge.CodeHash, expectedHash) {
		return errors.New("invalid email verification code")
	}
	if _, err = col.DeleteOne(ctx, bson.M{"_id": challenge.ID}); err != nil {
		return errors.Wrap(err, "delete consumed email verification code")
	}

	return nil
}

// ValidateEmailCodeLogin validates a passwordless email-code login attempt.
// It accepts account and email code, returning the matching user on success.
func (s *Blog) ValidateEmailCodeLogin(ctx context.Context, account string, code string) (*model.User, error) {
	account, err := sanitizeUserAccount(account)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize account")
	}
	if err = s.ConsumeEmailVerificationCode(ctx, account, model.EmailVerificationPurposeLogin, code); err != nil {
		return nil, errors.WithStack(model.ErrInvalidCredentials)
	}

	user, err := s.FindUserByAccount(ctx, account)
	if err != nil {
		return nil, errors.WithStack(model.ErrInvalidCredentials)
	}
	if user.Status != model.UserStatusActive {
		return nil, errors.WithStack(model.ErrInvalidCredentials)
	}

	return user, nil
}

// setupEmailVerificationCols creates indexes for SSO email verification challenges.
// It accepts a context and returns an error when index creation fails.
func (s *Blog) setupEmailVerificationCols(ctx context.Context) error {
	col := s.dao.GetEmailVerificationCodesCol()
	if _, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "account", Value: 1},
			{Key: "purpose", Value: 1},
		},
	}); err != nil {
		return errors.Wrap(err, "create email verification account index")
	}
	if _, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "expires_at", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(0),
	}); err != nil {
		return errors.Wrap(err, "create email verification ttl index")
	}
	return nil
}

// shouldSendEmailVerificationCode reports whether a code should be sent for a purpose.
// It accepts account and purpose, returning false for non-existent login accounts.
func (s *Blog) shouldSendEmailVerificationCode(ctx context.Context, account string, purpose string) (bool, error) {
	user, err := s.FindUserByAccount(ctx, account)
	if purpose == model.EmailVerificationPurposeRegister {
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return true, nil
			}
			return false, errors.Wrap(err, "find registration account")
		}
		if user != nil {
			return false, errors.New("account already exists")
		}
		return true, nil
	}

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, errors.Wrap(err, "find login account")
	}
	return user.Status == model.UserStatusActive, nil
}

// sanitizeEmailVerificationRequest validates account and purpose for email verification.
// It accepts raw account and purpose strings, returning normalized values.
func sanitizeEmailVerificationRequest(account string, purpose string) (string, string, error) {
	account, err := sanitizeUserAccount(account)
	if err != nil {
		return "", "", errors.Wrap(err, "sanitize account")
	}
	purpose = strings.TrimSpace(strings.ToLower(purpose))
	switch purpose {
	case model.EmailVerificationPurposeRegister, model.EmailVerificationPurposeLogin:
		return account, purpose, nil
	default:
		return "", "", errors.Errorf("unsupported email verification purpose %q", purpose)
	}
}

// generateEmailVerificationCode creates a six-digit decimal code.
// It accepts no parameters and returns the generated code string.
func generateEmailVerificationCode() (string, error) {
	limit := big.NewInt(1_000_000)
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", errors.Wrap(err, "generate random code")
	}
	return fmt.Sprintf("%0*d", emailVerificationCodeDigits, value.Int64()), nil
}

// hashEmailVerificationCode hashes code metadata with the configured application secret.
// It accepts account, purpose, and code strings, returning a hex HMAC digest.
func hashEmailVerificationCode(account string, purpose string, code string) string {
	secret := strings.TrimSpace(gconfig.Shared.GetString("settings.secret"))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(account))
	mac.Write([]byte{0})
	mac.Write([]byte(purpose))
	mac.Write([]byte{0})
	mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil))
}

// sendSMTPVerificationCode sends the verification code through the configured SMTP relay.
// It accepts the target account, code, and purpose, returning nil after the relay accepts it.
//
// The relay is configured under settings.web.smtp with host, port, username, and
// password. The username doubles as the From address; port 465 (the default)
// connects over implicit TLS.
func (s *Blog) sendSMTPVerificationCode(account string, code string, purpose string) error {
	host := strings.TrimSpace(gconfig.Shared.GetString("settings.web.smtp.host"))
	username := strings.TrimSpace(gconfig.Shared.GetString("settings.web.smtp.username"))
	password := gconfig.Shared.GetString("settings.web.smtp.password")
	if host == "" || username == "" || password == "" {
		return errors.New("smtp is not configured")
	}

	port := gconfig.Shared.GetInt("settings.web.smtp.port")
	if port == 0 {
		port = defaultSMTPTLSPort
	}
	senderName := strings.TrimSpace(gconfig.Shared.GetString("settings.web.smtp.sender_name"))

	subject := "Your Laisky SSO verification code"
	text := fmt.Sprintf("Your Laisky SSO %s code is %s. This code expires in 30 minutes.", purpose, code)

	mailer := email.NewMail(host, port)
	mailer.Login(username, password)

	var opts []email.SendOption
	if smtpDialerFactory != nil {
		opts = append(opts, email.WithMailSendDialer(smtpDialerFactory))
	}
	if err := mailer.Send(username, account, senderName, "", subject, text, opts...); err != nil {
		return errors.Wrap(err, "send smtp verification email")
	}

	return nil
}
