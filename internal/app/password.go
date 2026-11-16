package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"controle-gastos/internal/db"
)

// Password sign-in is for self-hosted installs without Google (APP_AUTH=password).
// Passwords are set only from the server's CLI, stored as argon2id hashes, and
// guarded by a per-account lockout on top of the per-IP rate limit.
const (
	MinPasswordLen = 12
	MaxPasswordLen = 128
	// MaxLoginFailures wrong passwords in a row lock the account for LockoutFor.
	MaxLoginFailures = 5
	LockoutFor       = 15 * time.Minute
)

// argon2id parameters: OWASP's minimum recommendation (19 MiB, 2 passes,
// 1 lane). Stored in the hash, so they can be raised later.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

// errBadLogin is the only answer to a failed password sign-in, whatever the
// reason (unknown email, wrong password, locked), so it reveals nothing.
var errBadLogin = invalid("e-mail ou senha incorretos, ou conta bloqueada por excesso de tentativas; aguarde 15 minutos")

// dummyHash makes unknown emails cost the same argon2 work as real ones.
var dummyHash = mustHash("not-a-real-password-just-timing")

func mustHash(pw string) string {
	h, err := hashPassword(pw)
	if err != nil {
		panic(err)
	}
	return h
}

// CheckPasswordPolicy validates a new password.
func CheckPasswordPolicy(email, pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < MinPasswordLen {
		return invalid("a senha precisa ter pelo menos %d caracteres", MinPasswordLen)
	}
	if len(pw) > MaxPasswordLen*4 || n > MaxPasswordLen {
		return invalid("a senha pode ter no máximo %d caracteres", MaxPasswordLen)
	}
	if strings.EqualFold(strings.TrimSpace(pw), strings.TrimSpace(email)) {
		return invalid("a senha não pode ser o seu e-mail")
	}
	return nil
}

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// verifyPassword checks pw against a PHC argon2id string in constant time.
func verifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var v int
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil || mem == 0 || mem > 1<<20 || t == 0 || t > 10 || p == 0 {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, mem, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// SetPassword creates or updates a password account (operator CLI). It ends
// the account's sessions.
func (a *App) SetPassword(ctx context.Context, email, pw string) (db.User, error) {
	email = strings.TrimSpace(email)
	if !strings.Contains(email, "@") || len(email) > 254 {
		return db.User{}, invalid("e-mail inválido")
	}
	if err := CheckPasswordPolicy(email, pw); err != nil {
		return db.User{}, err
	}
	h, err := hashPassword(pw)
	if err != nil {
		return db.User{}, err
	}
	u, err := a.db.SetPassword(ctx, email, h)
	if err == nil {
		Audit("password_set", "user_id", u.ID, "by", "operator-cli")
	}
	return u, err
}

// PasswordSignIn checks email and password and returns the actor, with the
// same error for every failure.
func (a *App) PasswordSignIn(ctx context.Context, email, pw, ip string) (Actor, error) {
	if len(pw) > MaxPasswordLen*4 || len(email) > 254 {
		return Actor{}, errBadLogin
	}
	acc, ok, err := a.db.PasswordAccountFor(ctx, email)
	if err != nil {
		return Actor{}, err
	}
	if !ok {
		verifyPassword(pw, dummyHash)
		Audit("login_failure", "reason", "unknown_account", "ip", ip)
		return Actor{}, errBadLogin
	}
	now := a.now()
	if acc.LockedUntil.After(now) {
		verifyPassword(pw, dummyHash)
		Audit("login_failure", "reason", "locked", "user_id", acc.User.ID, "ip", ip)
		return Actor{}, errBadLogin
	}
	if !verifyPassword(pw, acc.Hash) {
		locked, err := a.db.LoginFailed(ctx, acc.User.ID, MaxLoginFailures, now.Add(LockoutFor))
		if err != nil {
			return Actor{}, err
		}
		Audit("login_failure", "reason", "bad_password", "user_id", acc.User.ID, "ip", ip)
		if locked {
			Audit("account_locked", "user_id", acc.User.ID, "ip", ip)
		}
		return Actor{}, errBadLogin
	}
	if err := a.db.LoginSucceeded(ctx, acc.User.ID); err != nil {
		return Actor{}, err
	}
	return a.activate(ctx, acc.User)
}
