package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xlzd/gotp"
)

// TestVerifyTOTPCodeAcceptsCurrentCode verifies a generated current code validates successfully.
func TestVerifyTOTPCodeAcceptsCurrentCode(t *testing.T) {
	secret := newTOTPSecret()
	require.NotEmpty(t, secret)

	code := gotp.NewDefaultTOTP(secret).Now()
	require.True(t, verifyTOTPCode(secret, code))
}

// TestVerifyTOTPCodeRejectsInvalidCode verifies malformed and mismatched codes fail validation.
func TestVerifyTOTPCodeRejectsInvalidCode(t *testing.T) {
	secret := newTOTPSecret()
	require.NotEmpty(t, secret)

	code := gotp.NewDefaultTOTP(secret).Now()
	wrongCode := "000000"
	if code == wrongCode {
		wrongCode = "111111"
	}

	require.False(t, verifyTOTPCode(secret, wrongCode))
	require.False(t, verifyTOTPCode("", "000000"))
	require.False(t, verifyTOTPCode("not-base32", "000000"))
}
