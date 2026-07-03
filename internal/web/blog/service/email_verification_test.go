package service

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/go-utils/v6/email"
	"github.com/stretchr/testify/require"
	gomail "gopkg.in/gomail.v2"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestGenerateEmailVerificationCode verifies generated codes are six decimal digits.
func TestGenerateEmailVerificationCode(t *testing.T) {
	code, err := generateEmailVerificationCode()
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^\d{6}$`), code)
}

// TestSanitizeEmailVerificationRequest verifies account and purpose normalization.
func TestSanitizeEmailVerificationRequest(t *testing.T) {
	account, purpose, err := sanitizeEmailVerificationRequest(" Alice@Example.COM ", " Login ")
	require.NoError(t, err)
	require.Equal(t, "alice@example.com", account)
	require.Equal(t, model.EmailVerificationPurposeLogin, purpose)

	_, _, err = sanitizeEmailVerificationRequest("alice@example.com", "reset")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported email verification purpose")
}

// TestHashEmailVerificationCode verifies code hashes are deterministic and purpose-scoped.
func TestHashEmailVerificationCode(t *testing.T) {
	originalSecret := gconfig.Shared.GetString("settings.secret")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.secret", originalSecret)
	})
	gconfig.Shared.Set("settings.secret", "email-code-test-secret")

	hash := hashEmailVerificationCode("alice@example.com", model.EmailVerificationPurposeLogin, "123456")
	require.Equal(t, hash, hashEmailVerificationCode("alice@example.com", model.EmailVerificationPurposeLogin, "123456"))
	require.NotEqual(t, hash, hashEmailVerificationCode("alice@example.com", model.EmailVerificationPurposeRegister, "123456"))
	require.NotEqual(t, hash, hashEmailVerificationCode("alice@example.com", model.EmailVerificationPurposeLogin, "654321"))
}

// fakeSMTPSender records the messages handed to DialAndSend without contacting a relay.
type fakeSMTPSender struct {
	messages []*gomail.Message
}

// DialAndSend captures composed messages so tests can assert on the envelope and body.
func (f *fakeSMTPSender) DialAndSend(m ...*gomail.Message) error {
	f.messages = append(f.messages, m...)
	return nil
}

// TestSendSMTPVerificationCode verifies SMTP messages carry the expected envelope and body.
func TestSendSMTPVerificationCode(t *testing.T) {
	originalHost := gconfig.Shared.GetString("settings.web.smtp.host")
	originalPort := gconfig.Shared.GetInt("settings.web.smtp.port")
	originalUsername := gconfig.Shared.GetString("settings.web.smtp.username")
	originalPassword := gconfig.Shared.GetString("settings.web.smtp.password")
	originalSenderName := gconfig.Shared.GetString("settings.web.smtp.sender_name")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.web.smtp.host", originalHost)
		gconfig.Shared.Set("settings.web.smtp.port", originalPort)
		gconfig.Shared.Set("settings.web.smtp.username", originalUsername)
		gconfig.Shared.Set("settings.web.smtp.password", originalPassword)
		gconfig.Shared.Set("settings.web.smtp.sender_name", originalSenderName)
		smtpDialerFactory = nil
	})

	gconfig.Shared.Set("settings.web.smtp.host", "smtp.example.com")
	gconfig.Shared.Set("settings.web.smtp.port", 465)
	gconfig.Shared.Set("settings.web.smtp.username", "sso@example.com")
	gconfig.Shared.Set("settings.web.smtp.password", "smtp-password")
	gconfig.Shared.Set("settings.web.smtp.sender_name", "Laisky SSO")

	sender := &fakeSMTPSender{}
	smtpDialerFactory = func(host string, port int, username, passwd string) email.Sender {
		require.Equal(t, "smtp.example.com", host)
		require.Equal(t, 465, port)
		require.Equal(t, "sso@example.com", username)
		require.Equal(t, "smtp-password", passwd)
		return sender
	}

	svc := &Blog{logger: log.Logger.Named("test")}
	err := svc.sendSMTPVerificationCode("alice@example.com", "123456", model.EmailVerificationPurposeRegister)
	require.NoError(t, err)
	require.Len(t, sender.messages, 1)

	from := sender.messages[0].GetHeader("From")
	require.Len(t, from, 1)
	require.Contains(t, from[0], "sso@example.com")
	require.Contains(t, from[0], "Laisky SSO")
	require.Equal(t, []string{"alice@example.com"}, sender.messages[0].GetHeader("To"))
	require.Equal(t, []string{"Your Laisky SSO verification code"}, sender.messages[0].GetHeader("Subject"))

	var buf bytes.Buffer
	_, err = sender.messages[0].WriteTo(&buf)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "Your Laisky SSO register code is 123456")
}

// TestSendSMTPVerificationCodeRequiresConfig verifies SMTP settings are required.
func TestSendSMTPVerificationCodeRequiresConfig(t *testing.T) {
	originalHost := gconfig.Shared.GetString("settings.web.smtp.host")
	originalUsername := gconfig.Shared.GetString("settings.web.smtp.username")
	originalPassword := gconfig.Shared.GetString("settings.web.smtp.password")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.web.smtp.host", originalHost)
		gconfig.Shared.Set("settings.web.smtp.username", originalUsername)
		gconfig.Shared.Set("settings.web.smtp.password", originalPassword)
	})
	gconfig.Shared.Set("settings.web.smtp.host", "")
	gconfig.Shared.Set("settings.web.smtp.username", "")
	gconfig.Shared.Set("settings.web.smtp.password", "")

	svc := &Blog{logger: log.Logger.Named("test")}
	err := svc.sendSMTPVerificationCode(strings.Repeat("a", 5)+"@example.com", "123456", model.EmailVerificationPurposeLogin)
	require.Error(t, err)
	require.Contains(t, err.Error(), "smtp is not configured")
}
