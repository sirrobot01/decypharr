package nntp

import (
	"bytes"
	"context"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/testutil/nntpd"
)

func TestExecuteWithFailoverTriesAnotherBackboneAfterYencCorruption(t *testing.T) {
	bad, err := nntpd.New(nntpd.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bad.Close)
	good, err := nntpd.New(nntpd.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(good.Close)

	payload := nntpd.Pattern(0, 64<<10)
	encoded := nntpd.Encode(payload, "movie.mkv", 1, int64(len(payload)), 0)
	corrupt := bytes.Clone(encoded)
	firstEnd := bytes.Index(corrupt, []byte("\r\n"))
	if firstEnd < 0 {
		t.Fatal("could not locate encoded payload")
	}
	secondEnd := bytes.Index(corrupt[firstEnd+2:], []byte("\r\n"))
	if secondEnd < 0 {
		t.Fatal("could not locate encoded payload")
	}
	dataStart := firstEnd + 2 + secondEnd + 2
	if dataStart >= len(corrupt) {
		t.Fatal("could not locate encoded payload")
	}
	corrupt[dataStart]++

	const messageID = "<corrupt-failover@nntpd>"
	bad.AddArticle(messageID, corrupt)
	good.AddArticle(messageID, encoded)
	badHost, badPort := bad.Addr()
	goodHost, goodPort := good.Addr()
	client, err := NewClient(&config.Config{Usenet: config.Usenet{Providers: []config.UsenetProvider{
		{Host: badHost, Port: badPort, MaxConnections: 1, Priority: 1, Backbone: "bad-copy"},
		{Host: goodHost, Port: goodPort, MaxConnections: 1, Priority: 2, Backbone: "good-copy"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	var decoded []byte
	err = client.ExecuteWithFailover(context.Background(), func(conn *Connection) error {
		var fetchErr error
		decoded, fetchErr = conn.GetDecodedBody(messageID)
		return fetchErr
	})
	if err != nil {
		t.Fatalf("failover did not recover from corrupt provider copy: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatal("decoded payload came from neither verified article")
	}
	if bad.Bodies.Load() != 1 || good.Bodies.Load() != 1 {
		t.Fatalf("BODY counts bad=%d good=%d, want one per backbone", bad.Bodies.Load(), good.Bodies.Load())
	}
}
