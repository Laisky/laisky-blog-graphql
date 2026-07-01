package service

import (
	"strings"
	"time"

	gutils "github.com/Laisky/go-utils/v6"
	"github.com/xlzd/gotp"
)

const (
	totpSecretBytes = 20
	totpIssuerName  = "Laisky SSO"
)

// newTOTPSecret creates a base32 TOTP secret suitable for authenticator apps.
// It accepts no parameters and returns an empty string only if secure random generation fails.
func newTOTPSecret() string {
	return gotp.RandomSecret(totpSecretBytes)
}

// buildTOTPProvisioningURI creates an otpauth URI for authenticator enrollment.
// It accepts an account identifier and TOTP secret, returning the provisioning URI.
func buildTOTPProvisioningURI(account string, secret string) string {
	return gotp.NewDefaultTOTP(secret).ProvisioningUri(account, totpIssuerName)
}

// verifyTOTPCode validates a TOTP code against the current UTC time with one-step skew.
// It accepts a base32 secret and raw code, returning true when the code is valid.
func verifyTOTPCode(secret string, code string) bool {
	trimmedSecret := strings.TrimSpace(secret)
	trimmedCode := strings.TrimSpace(code)
	if trimmedSecret == "" || trimmedCode == "" || !gotp.IsSecretValid(trimmedSecret) {
		return false
	}

	totp := gotp.NewDefaultTOTP(trimmedSecret)
	now := gutils.Clock.GetUTCNow()
	for _, ts := range []time.Time{
		now.Add(-30 * time.Second),
		now,
		now.Add(30 * time.Second),
	} {
		if totp.VerifyTime(trimmedCode, ts) {
			return true
		}
	}

	return false
}
