package config

import (
	"testing"
)

func TestUsenetProviderID(t *testing.T) {
	a := UsenetProvider{Host: "news.example.com", Port: 563, Username: "alice"}
	b := UsenetProvider{Host: "news.example.com", Port: 563, Username: "bob"}
	c := UsenetProvider{Host: "news.example.com", Port: 119, Username: "alice"}

	if a.ID() == b.ID() {
		t.Fatal("same host with different accounts must have distinct IDs")
	}
	if a.ID() == c.ID() {
		t.Fatal("same host with different ports must have distinct IDs")
	}
	if a.ID() != (UsenetProvider{Host: "news.example.com", Port: 563, Username: "alice"}).ID() {
		t.Fatal("ID must be deterministic")
	}
}

func TestValidateUsenetRejectsDuplicateProvider(t *testing.T) {
	dup := UsenetProvider{Host: "news.example.com", Port: 563, Username: "alice", Password: "pw"}
	if err := validateUsenet([]UsenetProvider{dup, dup}); err == nil {
		t.Fatal("expected duplicate provider to be rejected")
	}

	// Dual accounts on the same host are legitimate and must pass.
	second := dup
	second.Username = "bob"
	if err := validateUsenet([]UsenetProvider{dup, second}); err != nil {
		t.Fatalf("dual-account setup rejected: %v", err)
	}
}

func TestUsenetDiskPathSelectsBufferStorage(t *testing.T) {
	var c Config
	c.updateUsenetConfig()
	if c.Usenet.DiskPath != "" {
		t.Fatalf("default disk path = %q, want empty", c.Usenet.DiskPath)
	}
	if c.Usenet.UsesDiskBuffer() {
		t.Fatal("empty disk path should use memory buffering")
	}

	c.Usenet.DiskPath = "  "
	if c.Usenet.UsesDiskBuffer() {
		t.Fatal("whitespace disk path should use memory buffering")
	}

	c.Usenet.DiskPath = "/cache/usenet"
	if !c.Usenet.UsesDiskBuffer() {
		t.Fatal("non-empty disk path should use disk buffering")
	}
}

func TestApplyUsenetEnvVarsDiskPath(t *testing.T) {
	t.Setenv("DECYPHARR_USENET__DISK_PATH", "/cache/usenet")

	var c Config
	c.applyUsenetEnvVars()
	if c.Usenet.DiskPath != "/cache/usenet" {
		t.Fatalf("environment disk path = %q, want /cache/usenet", c.Usenet.DiskPath)
	}
}
