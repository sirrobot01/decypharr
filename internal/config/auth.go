package config

import (
	"crypto/subtle"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

func VerifyAuth(username, password string) bool {
	// If you're storing hashed password, use bcrypt to compare
	if username == "" {
		return false
	}
	auth := Get().GetAuth()
	if auth == nil {
		return false
	}
	if username != auth.Username {
		return false
	}
	err := bcrypt.CompareHashAndPassword([]byte(auth.Password), []byte(password))
	return err == nil
}

// VerifyToken reports whether token matches the configured API token.
//
// This is kept out of VerifyAuth on purpose. The token authenticates the HTTP
// API surfaces (web API, qBittorrent, SABnzbd) only; WebDAV goes through
// VerifyAuth and must never be unlocked by an API token.
func VerifyToken(token string) bool {
	if token == "" {
		return false
	}
	auth := Get().GetAuth()
	if auth == nil || auth.APIToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(auth.APIToken)) == 1
}

// SetCredentials stores username and a bcrypt hash of password, enables auth,
// and persists auth.json. It is the only place credentials are written: the
// setup wizard, the registration page, and the settings page all go through it.
//
// The API token is left alone, but token-only mode ends — a password now exists.
func (c *Config) SetCredentials(username, password string) error {
	if username == "" {
		return errors.New("username is required")
	}
	if password == "" {
		return errors.New("password is required")
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	// Enable auth first, so GetAuth loads any existing auth.json instead of
	// returning nil and dropping the stored API token.
	c.UseAuth = true
	auth := c.GetAuth()
	if auth == nil {
		auth = &Auth{}
	}
	auth.Username = username
	auth.Password = string(hashed)
	auth.TokenOnly = false
	return c.SaveAuth(auth)
}
