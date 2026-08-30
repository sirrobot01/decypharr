package config

import (
	"testing"
)

func TestNFSDefaults(t *testing.T) {
	cfg := Config{NFS: NFS{Enabled: true}}
	cfg.setNFSDefaults()

	if cfg.NFS.BindAddress != "0.0.0.0" {
		t.Fatalf("bind address = %q", cfg.NFS.BindAddress)
	}
	if cfg.NFS.Port != DefaultNFSPort {
		t.Fatalf("port = %d, want %d", cfg.NFS.Port, DefaultNFSPort)
	}
	if len(cfg.NFS.AllowedNetworks) == 0 {
		t.Fatal("allowed networks were not defaulted")
	}
}

func TestNFSBindAddressInheritsServer(t *testing.T) {
	// Unset, it follows the server's bind address...
	cfg := Config{BindAddress: "192.168.1.10", NFS: NFS{Enabled: true}}
	cfg.setNFSDefaults()
	if cfg.NFS.BindAddress != "192.168.1.10" {
		t.Fatalf("bind address = %q, want inherited", cfg.NFS.BindAddress)
	}

	// ...but an explicit NFS bind address wins.
	cfg = Config{BindAddress: "192.168.1.10", NFS: NFS{Enabled: true, BindAddress: "127.0.0.1"}}
	cfg.setNFSDefaults()
	if cfg.NFS.BindAddress != "127.0.0.1" {
		t.Fatalf("bind address = %q, want explicit", cfg.NFS.BindAddress)
	}
}

func TestNFSDefaultsSkippedWhenDisabled(t *testing.T) {
	cfg := Config{}
	cfg.setNFSDefaults()
	if !cfg.NFS.IsZero() {
		t.Fatalf("disabled NFS gained defaults: %+v", cfg.NFS)
	}
}

func TestSMBDefaults(t *testing.T) {
	cfg := Config{SMB: SMB{Enabled: true, Username: "media", Password: "secret"}}
	cfg.setSMBDefaults()

	if cfg.SMB.BindAddress != "0.0.0.0" {
		t.Fatalf("bind address = %q", cfg.SMB.BindAddress)
	}
	if cfg.SMB.Port != DefaultSMBPort {
		t.Fatalf("port = %d, want %d", cfg.SMB.Port, DefaultSMBPort)
	}
	if cfg.SMB.ShareName != "decypharr" {
		t.Fatalf("share name = %q", cfg.SMB.ShareName)
	}
	if cfg.SMB.RequireSigning {
		t.Fatal("signing must not be required by default")
	}
	if len(cfg.SMB.AllowedNetworks) == 0 {
		t.Fatal("allowed networks were not defaulted")
	}

	disabled := Config{}
	disabled.setSMBDefaults()
	if !disabled.SMB.IsZero() {
		t.Fatalf("disabled SMB gained defaults: %+v", disabled.SMB)
	}
}
