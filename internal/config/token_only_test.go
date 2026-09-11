package config

import "testing"

func TestNeedsAuth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		useAuth bool
		auth    *Auth
		want    bool
	}{
		{name: "auth off", useAuth: false, auth: nil, want: false},
		{name: "auth on, nothing configured", useAuth: true, auth: nil, want: true},
		{name: "auth on, no password yet", useAuth: true, auth: &Auth{Username: "u"}, want: true},
		{name: "auth on, credentials set", useAuth: true, auth: &Auth{Username: "u", Password: "h"}, want: false},
		// A token alone is only a credential in token-only mode. Without the
		// flag, a fresh USE_AUTH install still has to register: setDefaults
		// mints a token before anyone has picked a password.
		{name: "token but not token-only", useAuth: true, auth: &Auth{APIToken: "tok"}, want: true},
		{name: "token-only with token", useAuth: true, auth: &Auth{APIToken: "tok", TokenOnly: true}, want: false},
		{name: "token-only without token", useAuth: true, auth: &Auth{TokenOnly: true}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{UseAuth: tc.useAuth, Auth: tc.auth}
			if got := c.NeedsAuth(); got != tc.want {
				t.Fatalf("NeedsAuth() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The API token authenticates the HTTP API surfaces only. VerifyAuth backs
// WebDAV, so it must keep rejecting the token no matter how it is presented.
func TestVerifyTokenIsNotAPassword(t *testing.T) {
	SetConfigPath(t.TempDir())
	t.Cleanup(Reset)

	c := Get()
	c.UseAuth = true
	if err := c.SaveAuth(&Auth{APIToken: "tok", TokenOnly: true}); err != nil {
		t.Fatal(err)
	}

	if !VerifyToken("tok") {
		t.Error("VerifyToken rejected the configured token")
	}
	for _, token := range []string{"", "wrong"} {
		if VerifyToken(token) {
			t.Errorf("VerifyToken(%q) accepted", token)
		}
	}

	for _, cred := range [][2]string{{"", "tok"}, {"tok", "tok"}, {"admin", "tok"}} {
		if VerifyAuth(cred[0], cred[1]) {
			t.Errorf("VerifyAuth(%q, %q) accepted the API token", cred[0], cred[1])
		}
	}

	// An unset token must never match an empty credential.
	if err := c.SaveAuth(&Auth{TokenOnly: true}); err != nil {
		t.Fatal(err)
	}
	if VerifyToken("") || VerifyAuth("", "") {
		t.Error("an empty credential authenticated with no token configured")
	}
}

func TestSetCredentials(t *testing.T) {
	Reset()
	SetConfigPath(t.TempDir())
	t.Cleanup(Reset)

	c := Get()
	c.UseAuth = true
	if err := c.SaveAuth(&Auth{APIToken: "tok", TokenOnly: true}); err != nil {
		t.Fatal(err)
	}

	for _, cred := range [][2]string{{"", "hunter2"}, {"admin", ""}} {
		if err := c.SetCredentials(cred[0], cred[1]); err == nil {
			t.Errorf("SetCredentials(%q, %q) accepted an empty field", cred[0], cred[1])
		}
	}

	if err := c.SetCredentials("admin", "hunter2"); err != nil {
		t.Fatal(err)
	}
	auth := c.GetAuth()
	if auth.TokenOnly {
		t.Error("token-only mode survived setting a password")
	}
	if auth.APIToken != "tok" {
		t.Errorf("APIToken = %q, want the existing token to be kept", auth.APIToken)
	}
	if !VerifyAuth("admin", "hunter2") {
		t.Error("the stored password does not verify")
	}
	if c.NeedsAuth() {
		t.Error("NeedsAuth() = true after credentials were set")
	}

	// Enabling auth from off must load auth.json rather than start from an
	// empty Auth, or the stored API token would be dropped.
	c.UseAuth = false
	c.Auth = nil
	if err := c.SetCredentials("admin", "hunter3"); err != nil {
		t.Fatal(err)
	}
	if !c.UseAuth {
		t.Error("UseAuth = false after setting credentials")
	}
	if got := c.GetAuth().APIToken; got != "tok" {
		t.Errorf("APIToken = %q after enabling auth, want %q", got, "tok")
	}
}
