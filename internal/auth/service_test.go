// SPDX-License-Identifier: GPL-2.0-or-later

package auth

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/vault"
	"github.com/pquerna/otp/totp"
)

var ctx = context.Background()

type fixture struct {
	s    *Service
	st   *store.SQLite
	mail *mail.Recorder
	now  time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	k, _ := vault.GenerateKey()
	kb := make([]byte, 32)
	copy(kb, k)
	v, _ := vault.New(kb)
	f := &fixture{st: st, mail: &mail.Recorder{}, now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	f.s = NewService(st, v, f.mail, "https://syntaxis.example.com")
	f.s.Now = func() time.Time { return f.now }
	return f
}

var tokRe = regexp.MustCompile(`token=([A-Za-z0-9_%\-]+)`)

func (f *fixture) lastToken(t *testing.T) string {
	t.Helper()
	m, ok := f.mail.Last()
	if !ok {
		t.Fatal("no mail sent")
	}
	g := tokRe.FindStringSubmatch(m.Body)
	if g == nil {
		t.Fatalf("no token in %q", m.Body)
	}
	tok, _ := url.QueryUnescape(g[1])
	return tok
}

const goodPW = "correct horse battery staple"

func (f *fixture) makeUser(t *testing.T, email string) *store.Account {
	t.Helper()
	id, err := f.s.InviteUser(ctx, 1, email, "Test Person")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CompleteToken(ctx, TokenInvite, f.lastToken(t), goodPW); err != nil {
		t.Fatal(err)
	}
	a, _ := f.st.GetAccount(ctx, store.KindUser, id)
	return a
}

func (f *fixture) makeAdmin(t *testing.T, email string) int64 {
	t.Helper()
	id, err := f.s.CreateAdmin(ctx, email, "Admin Person", goodPW)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) enableTOTP(t *testing.T, kind string, id int64) (secret string) {
	t.Helper()
	a, _ := f.st.GetAccount(ctx, kind, id)
	p := &Principal{Account: a}
	setup, err := f.s.BeginTOTP(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(setup.Secret, f.now)
	if _, err := f.s.ConfirmTOTP(ctx, p, code); err != nil {
		t.Fatal(err)
	}
	return setup.Secret
}

func TestInviteFlow(t *testing.T) {
	f := newFixture(t)
	id, err := f.s.InviteUser(ctx, 1, "New@Example.com", "New Person")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := f.mail.Last()
	if m.To != "new@example.com" || !strings.Contains(m.Body, "https://syntaxis.example.com/set-password?token=") {
		t.Fatalf("invite mail: %+v", m)
	}
	// cannot log in before setting a password
	if _, err := f.s.Login(ctx, store.KindUser, "new@example.com", goodPW, "1.1.1.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login before password: %v", err)
	}
	tok := f.lastToken(t)
	// weak password does not burn the token
	if _, err := f.s.CompleteToken(ctx, TokenInvite, tok, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak accepted: %v", err)
	}
	if _, err := f.s.CompleteToken(ctx, TokenInvite, tok, goodPW); err != nil {
		t.Fatalf("token burned by weak attempt: %v", err)
	}
	if _, err := f.s.CompleteToken(ctx, TokenInvite, tok, goodPW); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("single-use token reused")
	}
	if _, err := f.s.Login(ctx, store.KindUser, "new@example.com", goodPW, "1.1.1.1"); err != nil {
		t.Fatalf("login after invite: %v", err)
	}
	_ = id
}

func TestInviteValidationAndDuplicate(t *testing.T) {
	f := newFixture(t)
	if _, err := f.s.InviteUser(ctx, 1, "not-an-email", "x"); err == nil {
		t.Fatal("bad email accepted")
	}
	if _, err := f.s.InviteUser(ctx, 1, "a@example.com\r\nbcc:x@y.z", "x"); err == nil {
		t.Fatal("newline email accepted")
	}
	_, _ = f.s.InviteUser(ctx, 1, "a@example.com", "A")
	if _, err := f.s.InviteUser(ctx, 1, "a@example.com", "A"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestInviteExpiry(t *testing.T) {
	f := newFixture(t)
	_, _ = f.s.InviteUser(ctx, 1, "a@example.com", "Test Person")
	tok := f.lastToken(t)
	f.now = f.now.Add(InviteTTL + time.Minute)
	if _, err := f.s.CompleteToken(ctx, TokenInvite, tok, goodPW); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("expired invite accepted")
	}
	// admin can resend
	u, _ := f.st.GetAccountByEmail(ctx, store.KindUser, "a@example.com")
	if err := f.s.ResendInvite(ctx, 1, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CompleteToken(ctx, TokenInvite, f.lastToken(t), goodPW); err != nil {
		t.Fatalf("resent invite: %v", err)
	}
	if err := f.s.ResendInvite(ctx, 1, u.ID); err == nil {
		t.Fatal("resend allowed after password set")
	}
}

func TestTokenKindsNotInterchangeable(t *testing.T) {
	f := newFixture(t)
	_, _ = f.s.InviteUser(ctx, 1, "a@example.com", "Test Person")
	if _, err := f.s.CompleteToken(ctx, TokenReset, f.lastToken(t), goodPW); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("invite token accepted as reset")
	}
	if _, err := f.s.CompleteToken(ctx, "bogus", "x", goodPW); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("bogus kind")
	}
	if _, err := f.s.CompleteToken(ctx, TokenInvite, "garbage", goodPW); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("garbage token")
	}
}

func TestResetFlowAndNoEnumeration(t *testing.T) {
	f := newFixture(t)
	f.makeUser(t, "a@example.com")
	n := len(f.mail.Sent)
	// unknown account: nil error, no mail
	if err := f.s.RequestReset(ctx, "ghost@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(f.mail.Sent) != n {
		t.Fatal("mail sent for unknown account")
	}
	if err := f.s.RequestReset(ctx, "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(f.mail.Sent) != n+1 {
		t.Fatal("no reset mail")
	}
	old, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	tok := f.lastToken(t)
	if _, err := f.s.CompleteToken(ctx, TokenReset, tok, "an entirely new password!"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Authenticate(ctx, store.KindUser, old.SessionToken); err == nil {
		t.Fatal("old session survived password reset")
	}
	if _, err := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1"); err == nil {
		t.Fatal("old password still works")
	}
	if _, err := f.s.Login(ctx, store.KindUser, "a@example.com", "an entirely new password!", "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	// reset expiry
	_ = f.s.RequestReset(ctx, "a@example.com")
	tok = f.lastToken(t)
	f.now = f.now.Add(ResetTTL + time.Minute)
	if _, err := f.s.CompleteToken(ctx, TokenReset, tok, goodPW); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("expired reset accepted")
	}
}

func TestResetIgnoresDisabledAndUninvited(t *testing.T) {
	f := newFixture(t)
	u := f.makeUser(t, "a@example.com")
	u.Disabled = true
	_ = f.st.UpdateAccount(ctx, *u)
	n := len(f.mail.Sent)
	_ = f.s.RequestReset(ctx, "a@example.com")
	if len(f.mail.Sent) != n {
		t.Fatal("reset mailed to disabled user")
	}
	_, _ = f.s.InviteUser(ctx, 1, "b@example.com", "B Person")
	n = len(f.mail.Sent)
	_ = f.s.RequestReset(ctx, "b@example.com")
	if len(f.mail.Sent) != n {
		t.Fatal("reset must not bypass the invite for a user with no password")
	}
}

func TestAdminAndUserCredentialsSeparate(t *testing.T) {
	f := newFixture(t)
	f.makeAdmin(t, "boss@example.com")
	f.makeUser(t, "pal@example.com")
	// admin credentials do not work on the user login, and vice versa
	if _, err := f.s.Login(ctx, store.KindUser, "boss@example.com", goodPW, "1.1.1.1"); err == nil {
		t.Fatal("admin logged into chat login")
	}
	if _, err := f.s.Login(ctx, store.KindAdmin, "pal@example.com", goodPW, "1.1.1.1"); err == nil {
		t.Fatal("chat user logged into admin login")
	}
	ar, err := f.s.Login(ctx, store.KindAdmin, "boss@example.com", goodPW, "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	ur, _ := f.s.Login(ctx, store.KindUser, "pal@example.com", goodPW, "1.1.1.1")
	// cookies are not interchangeable
	if _, err := f.s.Authenticate(ctx, store.KindUser, ar.SessionToken); err == nil {
		t.Fatal("admin cookie accepted as chat session")
	}
	if _, err := f.s.Authenticate(ctx, store.KindAdmin, ur.SessionToken); err == nil {
		t.Fatal("chat cookie accepted as admin session")
	}
	if _, err := f.s.Authenticate(ctx, store.KindAdmin, ar.SessionToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Login(ctx, "root", "x", "y", "z"); !errors.Is(err, ErrForbidden) {
		t.Fatal("unknown kind")
	}
}

func TestAdminMustSetup2FA(t *testing.T) {
	f := newFixture(t)
	id := f.makeAdmin(t, "boss@example.com")
	r, _ := f.s.Login(ctx, store.KindAdmin, "boss@example.com", goodPW, "1.1.1.1")
	if !r.MustSetup2FA || r.NeedsMFA {
		t.Fatalf("admin without 2FA: %+v", r)
	}
	f.enableTOTP(t, store.KindAdmin, id)
	r, _ = f.s.Login(ctx, store.KindAdmin, "boss@example.com", goodPW, "1.1.1.1")
	if r.MustSetup2FA || !r.NeedsMFA {
		t.Fatalf("admin with 2FA: %+v", r)
	}
	// admin cannot disable 2FA
	a, _ := f.st.GetAccount(ctx, store.KindAdmin, id)
	if err := f.s.DisableTOTP(ctx, &Principal{Account: a}, goodPW); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin disabled 2FA: %v", err)
	}
}

func TestUserRemindedEveryLoginWithout2FA(t *testing.T) {
	f := newFixture(t)
	f.makeUser(t, "a@example.com")
	for i := 0; i < 3; i++ {
		r, err := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
		if err != nil || !r.Remind2FA {
			t.Fatalf("login %d: reminder dropped (%+v, %v)", i, r, err)
		}
	}
}

func TestPendingMFAAndVerify(t *testing.T) {
	f := newFixture(t)
	u := f.makeUser(t, "a@example.com")
	secret := f.enableTOTP(t, store.KindUser, u.ID)
	r, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	if !r.NeedsMFA || r.Remind2FA {
		t.Fatalf("%+v", r)
	}
	if _, err := f.s.Authenticate(ctx, store.KindUser, r.SessionToken); err == nil {
		t.Fatal("pending-MFA session grants access")
	}
	if _, err := f.s.VerifyMFA(ctx, store.KindUser, r.SessionToken, "000000", "1.1.1.1"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("bad code: %v", err)
	}
	f.now = f.now.Add(31 * time.Second) // new TOTP step (also defeats replay of the enrolment code)
	code, _ := totp.GenerateCode(secret, f.now)
	ok, err := f.s.VerifyMFA(ctx, store.KindUser, r.SessionToken, code, "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if ok.SessionToken == r.SessionToken {
		t.Fatal("session not rotated after MFA")
	}
	if _, err := f.s.Authenticate(ctx, store.KindUser, ok.SessionToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.VerifyMFA(ctx, store.KindUser, r.SessionToken, code, "1.1.1.1"); err == nil {
		t.Fatal("pending session reusable after completion")
	}
	// replay of the same code on a fresh login is rejected
	r2, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	if _, err := f.s.VerifyMFA(ctx, store.KindUser, r2.SessionToken, code, "1.1.1.1"); err == nil {
		t.Fatal("TOTP code replay accepted")
	}
}

func TestPendingMFAExpires(t *testing.T) {
	f := newFixture(t)
	u := f.makeUser(t, "a@example.com")
	secret := f.enableTOTP(t, store.KindUser, u.ID)
	r, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	f.now = f.now.Add(PendingMFATTL + time.Minute)
	code, _ := totp.GenerateCode(secret, f.now)
	if _, err := f.s.VerifyMFA(ctx, store.KindUser, r.SessionToken, code, "1.1.1.1"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired pending session: %v", err)
	}
}

func TestRecoveryCodeLogin(t *testing.T) {
	f := newFixture(t)
	u := f.makeUser(t, "a@example.com")
	a, _ := f.st.GetAccount(ctx, store.KindUser, u.ID)
	p := &Principal{Account: a}
	setup, _ := f.s.BeginTOTP(ctx, p)
	code, _ := totp.GenerateCode(setup.Secret, f.now)
	codes, err := f.s.ConfirmTOTP(ctx, p, code)
	if err != nil || len(codes) != 10 {
		t.Fatalf("%v %d", err, len(codes))
	}
	r, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	if _, err := f.s.VerifyMFA(ctx, store.KindUser, r.SessionToken, codes[0], "1.1.1.1"); err != nil {
		t.Fatalf("recovery code rejected: %v", err)
	}
	r, _ = f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	if _, err := f.s.VerifyMFA(ctx, store.KindUser, r.SessionToken, codes[0], "1.1.1.1"); err == nil {
		t.Fatal("recovery code reusable")
	}
}

func TestMFALockout(t *testing.T) {
	f := newFixture(t)
	u := f.makeUser(t, "a@example.com")
	f.enableTOTP(t, store.KindUser, u.ID)
	r, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	var last error
	for i := 0; i < MaxFailures+1; i++ {
		_, last = f.s.VerifyMFA(ctx, store.KindUser, r.SessionToken, "111111", "1.1.1.1")
	}
	if !errors.Is(last, ErrLocked) {
		t.Fatalf("MFA brute force not locked: %v", last)
	}
}

func TestTOTPSetupGuards(t *testing.T) {
	f := newFixture(t)
	u := f.makeUser(t, "a@example.com")
	a, _ := f.st.GetAccount(ctx, store.KindUser, u.ID)
	p := &Principal{Account: a}
	if _, err := f.s.ConfirmTOTP(ctx, p, "123456"); err == nil {
		t.Fatal("confirm without begin")
	}
	if _, err := f.s.BeginTOTP(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ConfirmTOTP(ctx, p, "000000"); !errors.Is(err, ErrInvalidCode) {
		t.Fatal("wrong confirmation code")
	}
	// the stored secret is encrypted, not plaintext
	got, _ := f.st.GetAccount(ctx, store.KindUser, u.ID)
	if len(got.TOTPSecret) == 0 || got.TOTPEnabled {
		t.Fatal("pending secret state")
	}
}

func TestDisableTOTP(t *testing.T) {
	f := newFixture(t)
	u := f.makeUser(t, "a@example.com")
	f.enableTOTP(t, store.KindUser, u.ID)
	a, _ := f.st.GetAccount(ctx, store.KindUser, u.ID)
	p := &Principal{Account: a}
	if err := f.s.DisableTOTP(ctx, p, "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("disable without password")
	}
	if err := f.s.DisableTOTP(ctx, p, goodPW); err != nil {
		t.Fatal(err)
	}
	g, _ := f.st.GetAccount(ctx, store.KindUser, u.ID)
	if g.TOTPEnabled || len(g.TOTPSecret) != 0 {
		t.Fatal("2FA still on")
	}
}

func TestLockoutPerAccountAndIP(t *testing.T) {
	f := newFixture(t)
	f.makeUser(t, "a@example.com")
	for i := 0; i < MaxFailures; i++ {
		if _, err := f.s.Login(ctx, store.KindUser, "a@example.com", "wrong-password-x", "9.9.9.9"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// correct password is refused while locked
	if _, err := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "2.2.2.2"); !errors.Is(err, ErrLocked) {
		t.Fatalf("account lockout: %v", err)
	}
	// IP lock affects other accounts from the same IP
	if _, err := f.s.Login(ctx, store.KindUser, "other@example.com", goodPW, "9.9.9.9"); !errors.Is(err, ErrLocked) {
		t.Fatalf("ip lockout: %v", err)
	}
	// lockout expires
	f.now = f.now.Add(LockoutDuration + time.Second)
	if _, err := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "2.2.2.2"); err != nil {
		t.Fatalf("after lockout: %v", err)
	}
}

func TestLockoutOnUnknownAccountToo(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < MaxFailures; i++ {
		_, _ = f.s.Login(ctx, store.KindUser, "ghost@example.com", "x", "9.9.9.9")
	}
	_, err := f.s.Login(ctx, store.KindUser, "ghost@example.com", "x", "8.8.8.8")
	if !errors.Is(err, ErrLocked) {
		t.Fatal("nonexistent accounts must lock like real ones (no enumeration)")
	}
}

func TestSuccessfulLoginClearsAccountFailures(t *testing.T) {
	f := newFixture(t)
	f.makeUser(t, "a@example.com")
	for i := 0; i < MaxFailures-1; i++ {
		_, _ = f.s.Login(ctx, store.KindUser, "a@example.com", "bad-password-xx", "3.3.3.3")
	}
	if _, err := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "4.4.4.4"); err != nil {
		t.Fatal(err)
	}
	_, _ = f.s.Login(ctx, store.KindUser, "a@example.com", "bad-password-xx", "4.4.4.4")
	if _, err := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "5.5.5.5"); err != nil {
		t.Fatalf("failure counter not cleared: %v", err)
	}
}

func TestDisabledAccountCannotLogin(t *testing.T) {
	f := newFixture(t)
	u := f.makeUser(t, "a@example.com")
	r, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	u.Disabled = true
	_ = f.st.UpdateAccount(ctx, *u)
	if _, err := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("disabled user logged in")
	}
	if _, err := f.s.Authenticate(ctx, store.KindUser, r.SessionToken); err == nil {
		t.Fatal("disabled user's live session still valid")
	}
}

func TestRotationPolicy(t *testing.T) {
	f := newFixture(t)
	f.s.Rotation = func(context.Context) int { return 30 }
	u := f.makeUser(t, "a@example.com") // password set "now"
	day := 24 * time.Hour

	r, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	if r.MustChangePassword || r.PasswordDaysLeft != -1 {
		t.Fatalf("fresh password: %+v", r)
	}
	f.now = f.now.Add(27 * day) // 3 days left -> warn
	r, _ = f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	if r.MustChangePassword || r.PasswordDaysLeft != 3 {
		t.Fatalf("warning window: %+v", r)
	}
	f.now = f.now.Add(4 * day) // expired
	r, _ = f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	if !r.MustChangePassword {
		t.Fatalf("expired password not forced: %+v", r)
	}
	// changing resets the clock
	a, _ := f.st.GetAccount(ctx, store.KindUser, u.ID)
	p, _ := f.s.Authenticate(ctx, store.KindUser, r.SessionToken)
	_ = a
	if err := f.s.ChangePassword(ctx, p, goodPW, "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	r, _ = f.s.Login(ctx, store.KindUser, "a@example.com", "a brand new passphrase", "1.1.1.1")
	if r.MustChangePassword {
		t.Fatal("still forced after change")
	}
}

func TestRotationExemptWith2FAAndDisabledByAdmin(t *testing.T) {
	f := newFixture(t)
	u := f.makeUser(t, "a@example.com")
	f.enableTOTP(t, store.KindUser, u.ID)
	f.now = f.now.Add(400 * 24 * time.Hour)
	a, _ := f.st.GetAccount(ctx, store.KindUser, u.ID)
	if must, _ := f.s.RotationStatus(ctx, a); must {
		t.Fatal("2FA users must be exempt from rotation")
	}
	f.s.Rotation = func(context.Context) int { return 0 }
	b := f.makeUser(t, "b@example.com")
	f.now = f.now.Add(400 * 24 * time.Hour)
	b, _ = f.st.GetAccount(ctx, store.KindUser, b.ID)
	if must, _ := f.s.RotationStatus(ctx, b); must {
		t.Fatal("rotation 0 must disable the policy")
	}
	ad := &store.Account{Kind: store.KindAdmin}
	if must, left := f.s.RotationStatus(ctx, ad); must || left != -1 {
		t.Fatal("admins are not subject to user rotation")
	}
}

func TestChangePasswordRules(t *testing.T) {
	f := newFixture(t)
	f.makeUser(t, "a@example.com")
	r, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	other, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.2")
	p, _ := f.s.Authenticate(ctx, store.KindUser, r.SessionToken)
	if err := f.s.ChangePassword(ctx, p, "wrong", "a brand new passphrase"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("wrong current")
	}
	if err := f.s.ChangePassword(ctx, p, goodPW, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatal("weak new")
	}
	if err := f.s.ChangePassword(ctx, p, goodPW, goodPW); !errors.Is(err, ErrWeakPassword) {
		t.Fatal("same password accepted")
	}
	if err := f.s.ChangePassword(ctx, p, goodPW, "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Authenticate(ctx, store.KindUser, other.SessionToken); err == nil {
		t.Fatal("other sessions survived a password change")
	}
}

func TestSessionLifecycle(t *testing.T) {
	f := newFixture(t)
	f.makeUser(t, "a@example.com")
	r, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	p, err := f.s.Authenticate(ctx, store.KindUser, r.SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	if !CheckCSRF(p.Session, r.CSRF) || CheckCSRF(p.Session, "") || CheckCSRF(p.Session, "wrong") {
		t.Fatal("csrf")
	}
	// rotation issues a new id and kills the old
	nt, ncsrf, err := f.s.Rotate(ctx, p, "1.1.1.1")
	if err != nil || nt == r.SessionToken || ncsrf == r.CSRF {
		t.Fatal("rotate")
	}
	if _, err := f.s.Authenticate(ctx, store.KindUser, r.SessionToken); err == nil {
		t.Fatal("old id valid after rotate")
	}
	// expiry
	f.now = f.now.Add(SessionTTL + time.Minute)
	if _, err := f.s.Authenticate(ctx, store.KindUser, nt); err == nil {
		t.Fatal("expired session valid")
	}
	if _, err := f.s.Authenticate(ctx, store.KindUser, ""); !errors.Is(err, ErrNoSession) {
		t.Fatal("empty")
	}
	// logout + logout everywhere
	f.now = f.now.Add(-SessionTTL)
	r1, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	r2, _ := f.s.Login(ctx, store.KindUser, "a@example.com", goodPW, "1.1.1.1")
	_ = f.s.Logout(ctx, r1.SessionToken)
	if _, err := f.s.Authenticate(ctx, store.KindUser, r1.SessionToken); err == nil {
		t.Fatal("logout")
	}
	p2, _ := f.s.Authenticate(ctx, store.KindUser, r2.SessionToken)
	_ = f.s.LogoutEverywhere(ctx, store.KindUser, p2.Account.ID)
	if _, err := f.s.Authenticate(ctx, store.KindUser, r2.SessionToken); err == nil {
		t.Fatal("logout everywhere")
	}
}

func TestCreateAdminPolicyAndAudit(t *testing.T) {
	f := newFixture(t)
	if _, err := f.s.CreateAdmin(ctx, "a@example.com", "A", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatal("weak admin password")
	}
	f.makeAdmin(t, "boss@example.com")
	es, _ := f.st.ListAudit(ctx, 0)
	if len(es) == 0 || es[0].Action != "admin.create" {
		t.Fatalf("audit: %+v", es)
	}
}

func TestNoSelfRegistrationAPI(t *testing.T) {
	// The service exposes no method that creates a chat user without an admin
	// actor id. InviteUser requires an admin; there is no Register function.
	f := newFixture(t)
	if _, err := f.s.Login(ctx, store.KindUser, "never-invited@example.com", goodPW, "1.1.1.1"); err == nil {
		t.Fatal("login for never-invited account")
	}
}
