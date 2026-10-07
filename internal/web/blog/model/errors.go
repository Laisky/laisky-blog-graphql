package model

import "github.com/Laisky/errors/v2"

// ErrInvalidCredentials indicates the login credentials are invalid.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrAccountExists indicates a registration collided with an existing account.
var ErrAccountExists = errors.New("account already exists")

// ErrTurnstileRequired indicates the client must complete a Turnstile challenge
// before the authentication request can proceed.
//
// It is returned only for clients the risk tracker considers suspicious (frequent
// requests or repeated failures), so low-risk clients are never challenged. The
// message is a stable token so the SSO web client can detect it and render the
// Turnstile widget as an extra step.
var ErrTurnstileRequired = errors.New("turnstile_required")

// ErrTOTPRequired indicates the account has TOTP enabled but no code was supplied.
//
// It is returned only after the password has been verified, so it never
// discloses whether an account exists or has TOTP enabled to callers that have
// not proven the password. The message is a stable token so the SSO web client
// can detect it and prompt for the TOTP code as a second step.
var ErrTOTPRequired = errors.New("totp_required")

// ErrTurnstileFailed indicates a Turnstile token was supplied but could not be
// verified, for example because it expired, was already used, or Cloudflare
// could not be reached. The client must solve a fresh challenge; it is never a
// statement about the submitted credentials.
var ErrTurnstileFailed = errors.New("turnstile_failed")

// ErrLoginUnavailable indicates authentication could not be evaluated because a
// backing dependency (database, signing key) failed. It is distinct from
// ErrInvalidCredentials so outages are neither reported to users as a wrong
// password nor counted toward the anti-abuse failure threshold.
var ErrLoginUnavailable = errors.New("login_unavailable")
