package share

import (
	"bytes"
	"net"
	"path/filepath"
	"testing"
)

func TestHandleKeyPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nfs", "handle.key")

	first, err := loadHandleKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 {
		t.Fatalf("key length = %d, want 32", len(first))
	}

	second, err := loadHandleKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("handle key changed between loads — client filehandles would go stale on restart")
	}
}

func TestAllowsFiltersNetworks(t *testing.T) {
	networks, err := parseNetworks([]string{"192.168.0.0/16", "127.0.0.1", "::1/128"})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		ip   string
		want bool
	}{
		{"192.168.4.20", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"10.1.2.3", false},
		{"8.8.8.8", false},
		{"::ffff:192.168.1.1", true}, // 4-in-6 mapped
	}
	for _, tc := range cases {
		addr := &net.TCPAddr{IP: net.ParseIP(tc.ip), Port: 1}
		if got := allows(networks, addr); got != tc.want {
			t.Errorf("allows(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestParseNetworksRejectsEmpty(t *testing.T) {
	if _, err := parseNetworks([]string{" ", ""}); err == nil {
		t.Fatal("expected an error for an empty network list")
	}
}
