// SPDX-License-Identifier: GPL-2.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/vault"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// Token kinds.
const (
	TokenInvite = "invite"
	TokenReset  = "reset"
)

// Tunables (var so tests can shorten them).
var (
	InviteTTL       = 72 * time.Hour
	ResetTTL        = time.Hour
	SessionTTL      = 12 * time.Hour
	PendingMFATTL   = 5 * time.Minute
	MaxFailures     = 5
	FailureWindow   = 15 * time.Minute
	LockoutDuration = 15 * time.Minute
	RotationWarn    = 5 * 24 * time.Hour
)

// Errors returned by the service. Login failures are deliberately uniform.
var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrLocked             = errors.New("too many attempts; try again later")
	ErrInvalidToken       = errors.New("invalid or expired link")
	ErrInvalidCode        = errors.New("invalid code")
	ErrNoSession          = errors.New("not signed in")
	ErrForbidden          = errors.New("forbidden")
)

// PolicyFunc returns the current password rotation interval in days (0 = off).
type PolicyFunc func(ctx context.Context) int

// Service implements auth flows for both account kinds.
type Service struct {
	Store    store.Store
	Vault    *vault.Vault
	Mail     mail.Mailer
	BaseURL  string
	Issuer   string
	Rotation PolicyFunc
	Now      func() time.Time

	mu          sync.Mutex
	lastCounter map[string]int64 // TOTP replay protection (per account)
}

// NewService builds a Service with defaults.
func NewService(st store.Store, v *vault.Vault, m mail.Mailer, baseURL string) *Service {
	return &Service{Store: st, Vault: v, Mail: m, BaseURL: strings.TrimRight(baseURL, "/"), Issuer: "Syntaxis",
		Rotation: func(context.Context) int { return 30 }, Now: time.Now, lastCounter: map[string]int64{}}
}

func (s *Service) now() time.Time { return s.Now().UTC() }

// ---- admin creation (CLI / bootstrap only, never web) ----

// CreateAdmin creates an admin account. Intended for the CLI and first boot.
func (s *Service) CreateAdmin(ctx context.Context, email, name, password string) (int64, error) {
	if err := CheckPasswordPolicy(password, email, name); err != nil {
		return 0, err
	}
	h, err := HashPassword(password)
	if err != nil {
		return 0, err
	}
	id, err := s.Store.CreateAccount(ctx, store.Account{Kind: store.KindAdmin, Email: email, Name: name, PasswordHash: h, CreatedAt: s.now(), PasswordChangedAt: s.now()})
	if err != nil {
		return 0, err
	}
	_, _ = s.Store.AppendAudit(ctx, "cli", "admin.create", fmt.Sprintf("admin:%d", id), email)
	return id, nil
}

// ---- invite / reset ----

// InviteUser creates a chat user without a password and emails an invite link.
func (s *Service) InviteUser(ctx context.Context, actorAdminID int64, email, name string) (int64, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") || strings.ContainsAny(email, " \r\n") {
		return 0, errors.New("invalid email")
	}
	id, err := s.Store.CreateAccount(ctx, store.Account{Kind: store.KindUser, Email: email, Name: name, CreatedAt: s.now()})
	if err != nil {
		return 0, err
	}
	if err := s.sendToken(ctx, id, email, name, TokenInvite, InviteTTL); err != nil {
		return id, err
	}
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("admin:%d", actorAdminID), "user.invite", fmt.Sprintf("user:%d", id), email)
	return id, nil
}

// ResendInvite issues a fresh invite for a user who has not set a password.
func (s *Service) ResendInvite(ctx context.Context, actorAdminID, userID int64) error {
	u, err := s.Store.GetAccount(ctx, store.KindUser, userID)
	if err != nil {
		return err
	}
	if u.PasswordHash != "" {
		return errors.New("user already set a password")
	}
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("admin:%d", actorAdminID), "user.invite.resend", fmt.Sprintf("user:%d", userID), "")
	return s.sendToken(ctx, userID, u.Email, u.Name, TokenInvite, InviteTTL)
}

func (s *Service) sendToken(ctx context.Context, userID int64, email, name, kind string, ttl time.Duration) error {
	tok, hash, err := NewToken()
	if err != nil {
		return err
	}
	if err := s.Store.PutToken(ctx, store.Token{Hash: hash, Kind: kind, UserID: userID, ExpiresAt: s.now().Add(ttl)}); err != nil {
		return err
	}
	link := s.BaseURL + "/set-password?token=" + url.QueryEscape(tok)
	subject, body := "You have been invited to Syntaxis", fmt.Sprintf("Hello %s,\n\nAn administrator created an account for you. Set your password here (valid %s, single use):\n\n%s\n", name, ttl, link)
	if kind == TokenReset {
		link = s.BaseURL + "/reset-password?token=" + url.QueryEscape(tok)
		subject, body = "Reset your Syntaxis password", fmt.Sprintf("Hello %s,\n\nUse this link to choose a new password (valid %s, single use). If you did not ask for this, ignore this email.\n\n%s\n", name, ttl, link)
	}
	return s.Mail.Send(ctx, mail.Message{To: email, Subject: subject, Body: body})
}

// RequestReset starts a password reset. It always returns nil so callers can
// give one generic response regardless of whether the account exists.
func (s *Service) RequestReset(ctx context.Context, email string) error {
	u, err := s.Store.GetAccountByEmail(ctx, store.KindUser, email)
	if err != nil || u.Disabled || u.PasswordHash == "" {
		return nil
	}
	return s.sendToken(ctx, u.ID, u.Email, u.Name, TokenReset, ResetTTL)
}

// CompleteToken consumes an invite or reset token and sets the password.
func (s *Service) CompleteToken(ctx context.Context, kind, token, newPassword string) (*store.Account, error) {
	if kind != TokenInvite && kind != TokenReset {
		return nil, ErrInvalidToken
	}
	// Validate the password BEFORE consuming the token so a weak attempt does not burn the link.
	hash := HashToken(token)
	tk, err := s.peekToken(ctx, hash, kind)
	if err != nil {
		return nil, ErrInvalidToken
	}
	u, err := s.Store.GetAccount(ctx, store.KindUser, tk.UserID)
	if err != nil || u.Disabled {
		return nil, ErrInvalidToken
	}
	if err := CheckPasswordPolicy(newPassword, u.Email, u.Name); err != nil {
		return nil, err
	}
	if _, err := s.Store.ConsumeToken(ctx, hash, kind, s.now()); err != nil {
		return nil, ErrInvalidToken
	}
	h, err := HashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	u.PasswordHash, u.PasswordChangedAt = h, s.now()
	if err := s.Store.UpdateAccount(ctx, *u); err != nil {
		return nil, err
	}
	_ = s.Store.DeleteAuthSessionsFor(ctx, store.KindUser, u.ID)
	_ = s.Store.ClearAttempt(ctx, attemptKeyAccount(store.KindUser, u.Email))
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("user:%d", u.ID), "password."+kind, fmt.Sprintf("user:%d", u.ID), "")
	return u, nil
}

// peekToken reads token validity without consuming it.
func (s *Service) peekToken(ctx context.Context, hash, kind string) (*store.Token, error) {
	return s.Store.PeekToken(ctx, hash, kind, s.now())
}

// ---- login ----

// LoginResult describes what the caller must do next.
type LoginResult struct {
	SessionToken       string // cookie value (opaque)
	CSRF               string
	Account            *store.Account
	NeedsMFA           bool // session is pending until VerifyMFA
	MustChangePassword bool // rotation expired: force change before anything else
	MustSetup2FA       bool // admins without 2FA
	Remind2FA          bool // users without 2FA: show prominent reminder every login
	PasswordDaysLeft   int  // >=0 when rotation applies and within warning window, else -1
}

func attemptKeyAccount(kind, email string) string { return "acct:" + kind + ":" + strings.ToLower(email) }
func attemptKeyIP(ip string) string               { return "ip:" + ip }

// Login verifies credentials for the given account kind. Admin and chat
// credentials never cross: kind selects the table.
func (s *Service) Login(ctx context.Context, kind, email, password, ip string) (*LoginResult, error) {
	if kind != store.KindAdmin && kind != store.KindUser {
		return nil, ErrForbidden
	}
	ka, ki := attemptKeyAccount(kind, email), attemptKeyIP(ip)
	if s.locked(ctx, ka) || s.locked(ctx, ki) {
		return nil, ErrLocked
	}
	acct, err := s.Store.GetAccountByEmail(ctx, kind, email)
	hash := dummyHash
	if err == nil && acct.PasswordHash != "" {
		hash = acct.PasswordHash
	}
	ok := VerifyPassword(password, hash) && err == nil && acct.PasswordHash != "" && !acct.Disabled
	if !ok {
		s.fail(ctx, ka)
		s.fail(ctx, ki)
		return nil, ErrInvalidCredentials
	}
	_ = s.Store.ClearAttempt(ctx, ka)
	res := &LoginResult{Account: acct, PasswordDaysLeft: -1}
	res.NeedsMFA = acct.TOTPEnabled
	if kind == store.KindAdmin && !acct.TOTPEnabled {
		res.MustSetup2FA = true
	}
	if kind == store.KindUser && !acct.TOTPEnabled {
		res.Remind2FA = true
	}
	if kind == store.KindUser && !res.NeedsMFA {
		res.MustChangePassword, res.PasswordDaysLeft = s.rotationState(ctx, acct)
	}
	ttl := SessionTTL
	if res.NeedsMFA {
		ttl = PendingMFATTL
	}
	if res.SessionToken, res.CSRF, err = s.newSession(ctx, kind, acct.ID, ip, ttl, res.NeedsMFA); err != nil {
		return nil, err
	}
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("%s:%d", kind, acct.ID), "login", fmt.Sprintf("%s:%d", kind, acct.ID), "ip="+ip)
	return res, nil
}

func (s *Service) locked(ctx context.Context, key string) bool {
	a, err := s.Store.GetAttempt(ctx, key)
	return err == nil && s.now().Before(a.LockedUntil)
}

func (s *Service) fail(ctx context.Context, key string) {
	a, err := s.Store.GetAttempt(ctx, key)
	now := s.now()
	if err != nil || now.Sub(a.WindowStart) > FailureWindow {
		a = &store.Attempt{Key: key, WindowStart: now}
	}
	a.Failures++
	if a.Failures >= MaxFailures {
		a.LockedUntil = now.Add(LockoutDuration)
		a.Failures, a.WindowStart = 0, now
		_, _ = s.Store.AppendAudit(ctx, "system", "lockout", key, "")
	}
	_ = s.Store.PutAttempt(ctx, *a)
}

// rotationState applies the admin-controlled rotation policy. 2FA users are exempt.
func (s *Service) rotationState(ctx context.Context, a *store.Account) (mustChange bool, daysLeft int) {
	days := s.Rotation(ctx)
	if days <= 0 || a.TOTPEnabled || a.PasswordChangedAt.IsZero() {
		return false, -1
	}
	expiry := a.PasswordChangedAt.Add(time.Duration(days) * 24 * time.Hour)
	left := expiry.Sub(s.now())
	if left <= 0 {
		return true, 0
	}
	if left <= RotationWarn {
		return false, int((left + 24*time.Hour - 1) / (24 * time.Hour))
	}
	return false, -1
}

// RotationStatus reports rotation state for an already-logged-in account.
func (s *Service) RotationStatus(ctx context.Context, a *store.Account) (mustChange bool, daysLeft int) {
	if a.Kind != store.KindUser {
		return false, -1
	}
	return s.rotationState(ctx, a)
}

func (s *Service) newSession(ctx context.Context, kind string, id int64, ip string, ttl time.Duration, pending bool) (token, csrf string, err error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", "", err
	}
	csrf, _, err = NewToken()
	if err != nil {
		return "", "", err
	}
	err = s.Store.PutAuthSession(ctx, store.AuthSession{IDHash: hash, Kind: kind, OwnerID: id, CSRF: csrf, CreatedAt: s.now(), ExpiresAt: s.now().Add(ttl), PendingMFA: pending, IP: ip})
	return token, csrf, err
}

// VerifyMFA completes a pending login using a TOTP or recovery code and
// returns a fresh (rotated) session.
func (s *Service) VerifyMFA(ctx context.Context, kind, sessionToken, code, ip string) (*LoginResult, error) {
	sess, err := s.Store.GetAuthSession(ctx, HashToken(sessionToken))
	if err != nil || sess.Kind != kind || !sess.PendingMFA || !s.now().Before(sess.ExpiresAt) {
		return nil, ErrNoSession
	}
	acct, err := s.Store.GetAccount(ctx, kind, sess.OwnerID)
	if err != nil || acct.Disabled {
		return nil, ErrNoSession
	}
	key := attemptKeyAccount(kind, acct.Email) + ":mfa"
	if s.locked(ctx, key) {
		return nil, ErrLocked
	}
	if !s.checkSecondFactor(ctx, acct, code) {
		s.fail(ctx, key)
		return nil, ErrInvalidCode
	}
	_ = s.Store.ClearAttempt(ctx, key)
	_ = s.Store.DeleteAuthSession(ctx, sess.IDHash)
	tok, csrf, err := s.newSession(ctx, kind, acct.ID, ip, SessionTTL, false)
	if err != nil {
		return nil, err
	}
	res := &LoginResult{SessionToken: tok, CSRF: csrf, Account: acct, PasswordDaysLeft: -1}
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("%s:%d", kind, acct.ID), "login.mfa", fmt.Sprintf("%s:%d", kind, acct.ID), "")
	return res, nil
}

func (s *Service) checkSecondFactor(ctx context.Context, a *store.Account, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	if s.verifyTOTP(a, code) {
		return true
	}
	ok, err := s.Store.UseRecoveryCode(ctx, a.Kind, a.ID, HashRecoveryCode(code))
	if ok && err == nil {
		_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("%s:%d", a.Kind, a.ID), "recovery.used", fmt.Sprintf("%s:%d", a.Kind, a.ID), "")
		return true
	}
	return false
}

func (s *Service) totpAAD(kind string, id int64) string { return fmt.Sprintf("totp:%s:%d", kind, id) }

func (s *Service) verifyTOTP(a *store.Account, code string) bool {
	if len(a.TOTPSecret) == 0 || len(code) != 6 {
		return false
	}
	secret, err := s.Vault.Open(a.TOTPSecret, s.totpAAD(a.Kind, a.ID))
	if err != nil {
		return false
	}
	now := s.now()
	ok, err := totp.ValidateCustom(code, string(secret), now, totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil || !ok {
		return false
	}
	// replay protection: a counter may be used once
	counter := now.Unix() / 30
	key := fmt.Sprintf("%s:%d", a.Kind, a.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastCounter[key] >= counter {
		return false
	}
	s.lastCounter[key] = counter
	return true
}

// ---- session validation ----

// Principal is an authenticated caller.
type Principal struct {
	Account *store.Account
	Session *store.AuthSession
}

// Authenticate validates a session cookie for the given kind. A cookie issued
// for one kind is never valid for the other.
func (s *Service) Authenticate(ctx context.Context, kind, sessionToken string) (*Principal, error) {
	if sessionToken == "" {
		return nil, ErrNoSession
	}
	sess, err := s.Store.GetAuthSession(ctx, HashToken(sessionToken))
	if err != nil || sess.Kind != kind || sess.PendingMFA || !s.now().Before(sess.ExpiresAt) {
		return nil, ErrNoSession
	}
	acct, err := s.Store.GetAccount(ctx, kind, sess.OwnerID)
	if err != nil || acct.Disabled {
		return nil, ErrNoSession
	}
	return &Principal{Account: acct, Session: sess}, nil
}

// CheckCSRF validates the CSRF token for a state-changing request.
func CheckCSRF(sess *store.AuthSession, header string) bool {
	return header != "" && EqualConstTime(sess.CSRF, header)
}

// Logout destroys one session.
func (s *Service) Logout(ctx context.Context, sessionToken string) error {
	return s.Store.DeleteAuthSession(ctx, HashToken(sessionToken))
}

// LogoutEverywhere revokes all sessions of an account.
func (s *Service) LogoutEverywhere(ctx context.Context, kind string, id int64) error {
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("%s:%d", kind, id), "logout.everywhere", fmt.Sprintf("%s:%d", kind, id), "")
	return s.Store.DeleteAuthSessionsFor(ctx, kind, id)
}

// Rotate replaces a session id (call after privilege changes).
func (s *Service) Rotate(ctx context.Context, p *Principal, ip string) (token, csrf string, err error) {
	token, csrf, err = s.newSession(ctx, p.Account.Kind, p.Account.ID, ip, SessionTTL, false)
	if err != nil {
		return "", "", err
	}
	return token, csrf, s.Store.DeleteAuthSession(ctx, p.Session.IDHash)
}

// ---- password change ----

// ChangePassword changes a password after verifying the current one, then
// revokes all other sessions.
func (s *Service) ChangePassword(ctx context.Context, p *Principal, current, next string) error {
	a := p.Account
	if !VerifyPassword(current, a.PasswordHash) {
		return ErrInvalidCredentials
	}
	if err := CheckPasswordPolicy(next, a.Email, a.Name); err != nil {
		return err
	}
	if VerifyPassword(next, a.PasswordHash) {
		return fmt.Errorf("%w: must differ from the current password", ErrWeakPassword)
	}
	h, err := HashPassword(next)
	if err != nil {
		return err
	}
	a.PasswordHash, a.PasswordChangedAt = h, s.now()
	if err := s.Store.UpdateAccount(ctx, *a); err != nil {
		return err
	}
	_ = s.Store.DeleteAuthSessionsFor(ctx, a.Kind, a.ID)
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("%s:%d", a.Kind, a.ID), "password.change", fmt.Sprintf("%s:%d", a.Kind, a.ID), "")
	return nil
}

// ---- TOTP enrolment ----

// TOTPSetup is returned when enrolment starts.
type TOTPSetup struct {
	Secret string // base32, show once
	URL    string // otpauth:// for QR
}

// BeginTOTP generates and stores (unconfirmed) a TOTP secret.
func (s *Service) BeginTOTP(ctx context.Context, p *Principal) (*TOTPSetup, error) {
	a := p.Account
	if a.TOTPEnabled {
		return nil, errors.New("2FA already enabled")
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: s.Issuer + " (" + a.Kind + ")", AccountName: a.Email})
	if err != nil {
		return nil, err
	}
	sealed, err := s.Vault.Seal([]byte(key.Secret()), s.totpAAD(a.Kind, a.ID))
	if err != nil {
		return nil, err
	}
	a.TOTPSecret = sealed
	if err := s.Store.UpdateAccount(ctx, *a); err != nil {
		return nil, err
	}
	return &TOTPSetup{Secret: key.Secret(), URL: key.URL()}, nil
}

// ConfirmTOTP verifies a first code, enables 2FA and returns recovery codes (shown once).
func (s *Service) ConfirmTOTP(ctx context.Context, p *Principal, code string) ([]string, error) {
	a := p.Account
	if a.TOTPEnabled || len(a.TOTPSecret) == 0 {
		return nil, errors.New("no pending 2FA setup")
	}
	if !s.verifyTOTP(a, strings.TrimSpace(code)) {
		return nil, ErrInvalidCode
	}
	codes, hashes, err := NewRecoveryCodes(10)
	if err != nil {
		return nil, err
	}
	if err := s.Store.ReplaceRecoveryCodes(ctx, a.Kind, a.ID, hashes); err != nil {
		return nil, err
	}
	a.TOTPEnabled = true
	if err := s.Store.UpdateAccount(ctx, *a); err != nil {
		return nil, err
	}
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("%s:%d", a.Kind, a.ID), "2fa.enable", fmt.Sprintf("%s:%d", a.Kind, a.ID), "")
	return codes, nil
}

// DisableTOTP turns 2FA off (users only; admins must keep 2FA).
func (s *Service) DisableTOTP(ctx context.Context, p *Principal, password string) error {
	a := p.Account
	if a.Kind == store.KindAdmin {
		return fmt.Errorf("%w: admin accounts require 2FA", ErrForbidden)
	}
	if !VerifyPassword(password, a.PasswordHash) {
		return ErrInvalidCredentials
	}
	a.TOTPEnabled, a.TOTPSecret = false, nil
	if err := s.Store.UpdateAccount(ctx, *a); err != nil {
		return err
	}
	_ = s.Store.ReplaceRecoveryCodes(ctx, a.Kind, a.ID, nil)
	_, _ = s.Store.AppendAudit(ctx, fmt.Sprintf("%s:%d", a.Kind, a.ID), "2fa.disable", fmt.Sprintf("%s:%d", a.Kind, a.ID), "")
	return nil
}
