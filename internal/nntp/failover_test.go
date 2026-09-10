package nntp

import (
	"errors"
	"slices"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/testutil/nntpd"
)

func TestFailoverAttributesMissingArticleToActualProvider(t *testing.T) {
	first, err := nntpd.New(nntpd.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := nntpd.New(nntpd.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	host, firstPort := first.Addr()
	_, secondPort := second.Addr()
	providers := []config.UsenetProvider{
		{Host: host, Port: firstPort, MaxConnections: 1, Priority: 1, Backbone: "first"},
		{Host: "localhost", Port: secondPort, MaxConnections: 1, Priority: 2, Backbone: "second"},
	}
	client, err := NewClient(&config.Config{Retries: 1, Usenet: config.Usenet{Providers: providers}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var visited []string
	err = client.ExecuteWithFailover(t.Context(), WorkloadStreamDemand, func(conn *Connection) error {
		backbone := conn.pool.config.Backbone
		visited = append(visited, backbone)
		if backbone == "second" {
			return &Error{Type: ErrorTypeArticleNotFound}
		}
		if len(visited) == 1 {
			return NewConnectionError(errors.New("connection failed"))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failover failed: %v", err)
	}
	if !slices.Equal(visited, []string{"first", "second", "first"}) {
		t.Fatalf("visited %v", visited)
	}
	for _, pp := range client.orderedPools {
		if len(pp.slots) != 0 {
			t.Fatalf("provider %s leaked an admission slot", pp.config.Host)
		}
	}
}

func TestFailoverHonorsSingleProviderRetryBudget(t *testing.T) {
	server, err := nntpd.New(nntpd.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	host, port := server.Addr()
	client, err := NewClient(&config.Config{Retries: 1, Usenet: config.Usenet{Providers: []config.UsenetProvider{{Host: host, Port: port, MaxConnections: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	calls := 0
	failure := NewTimeoutError(errors.New("timeout"))
	err = client.ExecuteWithFailover(t.Context(), WorkloadStreamDemand, func(*Connection) error { calls++; return failure })
	if calls != 2 || !errors.Is(err, ErrAllProvidersFailed) || !errors.Is(err, failure) {
		t.Fatalf("calls=%d, error=%v", calls, err)
	}
	if len(client.orderedPools[0].slots) != 0 {
		t.Fatal("retry budget exhaustion leaked an admission slot")
	}
}
