package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserRegisterInputLength(t *testing.T) {
	r := &MutationResolver{}
	ctx := context.Background()

	// Test account too long
	_, err := r.UserRegister(ctx, strings.Repeat("a", 101), "pass", "name", "captcha", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input too long")

	// Test password too long
	_, err = r.UserRegister(ctx, "account", strings.Repeat("p", 101), "name", "captcha", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input too long")

	// Test displayName too long
	_, err = r.UserRegister(ctx, "account", "pass", strings.Repeat("n", 101), "captcha", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input too long")

	// Test captcha too long
	_, err = r.UserRegister(ctx, "account", "pass", "name", strings.Repeat("c", 501), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input too long")
}

func TestBlogLoginInputLength(t *testing.T) {
	r := &MutationResolver{}
	ctx := context.Background()

	// Test account too long
	_, err := r.BlogLogin(ctx, strings.Repeat("a", 101), "pass")
	require.Error(t, err)
	require.Contains(t, err.Error(), "input too long")

	// Test password too long
	_, err = r.BlogLogin(ctx, "account", strings.Repeat("p", 101))
	require.Error(t, err)
	require.Contains(t, err.Error(), "input too long")
}

func TestValidateInputLengthRuneAware(t *testing.T) {
	require.NoError(t, validateInputLength(2, "😀😀"))

	err := validateInputLength(1, "😀😀")
	require.Error(t, err)
	require.Contains(t, err.Error(), "max 1 characters allowed")

	err = validateInputLength(100, strings.Repeat("😀", 101))
	require.Error(t, err)
	require.Contains(t, err.Error(), "max 100 characters allowed")
}

func TestUserRegisterInputLengthWithMultiByteCharacters(t *testing.T) {
	r := &MutationResolver{}
	ctx := context.Background()

	_, err := r.UserRegister(ctx, strings.Repeat("😀", 101), "pass", "name", "captcha", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input too long")
}
