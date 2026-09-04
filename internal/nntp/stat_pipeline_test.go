package nntp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestStatBatchPipelinesCommandsAndMapsResponses(t *testing.T) {
	conn, server := newBodyTestConn(t)
	messageIDs := []string{"a@example", "missing@example", "c@example"}
	serverErr := serveStatPipeline(server, len(messageIDs), []string{
		"223 0 <a@example>\r\n",
		"430 no such article\r\n",
		"223 0 <c@example>\r\n",
	})

	results, err := conn.StatBatch(messageIDs)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if len(results) != len(messageIDs) {
		t.Fatalf("got %d results, want %d", len(results), len(messageIDs))
	}
	if !results[0].Available || results[0].Error != nil {
		t.Fatalf("first result = %+v, want available", results[0])
	}
	if results[1].Available || !IsArticleNotFoundError(results[1].Error) {
		t.Fatalf("second result = %+v, want article-not-found", results[1])
	}
	if !results[2].Available || results[2].Error != nil {
		t.Fatalf("third result = %+v, want available", results[2])
	}
}

func TestBatchStatOnProviderYieldsToStreamBetweenPipelines(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	t.Cleanup(func() {
		_ = clientSide.Close()
		_ = serverSide.Close()
	})
	conn := &Connection{
		conn:   clientSide,
		reader: bufio.NewReader(clientSide),
		writer: bufio.NewWriter(clientSide),
	}
	pp := newTestPool(1)
	poolEntry(pp, conn, 0)
	client := newAcquireTestClient(pp)

	messageIDs := make([]string, statPipelineDepth+4)
	for i := range messageIDs {
		messageIDs[i] = fmt.Sprintf("article-%d@example", i)
	}
	firstWindowRead := make(chan struct{})
	allowFirstResponses := make(chan struct{})
	secondWindowRead := make(chan struct{})
	serverErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverSide)
		if err := readStatCommands(reader, statPipelineDepth); err != nil {
			serverErr <- err
			return
		}
		close(firstWindowRead)
		<-allowFirstResponses
		if err := writeStatResponses(serverSide, messageIDs[:statPipelineDepth]); err != nil {
			serverErr <- err
			return
		}
		if err := readStatCommands(reader, len(messageIDs)-statPipelineDepth); err != nil {
			serverErr <- err
			return
		}
		close(secondWindowRead)
		serverErr <- writeStatResponses(serverSide, messageIDs[statPipelineDepth:])
	}()

	resultErr := make(chan error, 1)
	go func() {
		results, err := client.batchStatOnProvider(t.Context(), pp.config, messageIDs)
		if err == nil && len(results) != len(messageIDs) {
			err = fmt.Errorf("got %d results, want %d", len(results), len(messageIDs))
		}
		resultErr <- err
	}()

	select {
	case <-firstWindowRead:
	case <-time.After(2 * time.Second):
		t.Fatal("background pipeline was not written")
	}

	streamAcquired := make(chan struct{})
	releaseStream := make(chan struct{})
	streamErr := make(chan error, 1)
	go func() {
		streamConn, provider, err := client.getAnyAvailableConnection(t.Context(), WorkloadStream, providerExclusions{})
		if err != nil {
			streamErr <- err
			return
		}
		close(streamAcquired)
		<-releaseStream
		client.put(streamConn, provider)
		streamErr <- nil
	}()
	waitForQueuedWorkload(t, client, WorkloadStream)
	close(allowFirstResponses)

	select {
	case <-streamAcquired:
	case <-secondWindowRead:
		t.Fatal("background work reacquired before the waiting stream")
	case <-time.After(2 * time.Second):
		t.Fatal("stream was not admitted after the first pipeline")
	}
	close(releaseStream)

	select {
	case <-secondWindowRead:
	case <-time.After(2 * time.Second):
		t.Fatal("background work did not resume after the stream yielded")
	}
	if err := <-streamErr; err != nil {
		t.Fatal(err)
	}
	if err := <-resultErr; err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestStatBatchMarksUnreadSuffixAfterDisconnect(t *testing.T) {
	conn, server := newBodyTestConn(t)
	messageIDs := []string{"a@example", "b@example", "c@example"}
	serverErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)
		for range messageIDs {
			if _, err := reader.ReadString('\n'); err != nil {
				serverErr <- err
				return
			}
		}
		if _, err := io.WriteString(server, "223 0 <a@example>\r\n"); err != nil {
			serverErr <- err
			return
		}
		serverErr <- server.Close()
	}()

	results, err := conn.StatBatch(messageIDs)
	if err == nil {
		t.Fatal("expected pipeline read failure")
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if !results[0].Available {
		t.Fatalf("first result = %+v, want available", results[0])
	}
	for i, result := range results[1:] {
		if result.Available {
			t.Fatalf("result %d unexpectedly available", i+1)
		}
		if nntpErr, ok := errors.AsType[*Error](result.Error); !ok || nntpErr.Type != ErrorTypeConnection {
			t.Fatalf("result %d error = %v, want connection error", i+1, result.Error)
		}
	}
}

func serveStatPipeline(server net.Conn, commands int, responses []string) <-chan error {
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)
		for i := range commands {
			line, err := reader.ReadString('\n')
			if err != nil {
				done <- err
				return
			}
			if !strings.HasPrefix(line, "STAT <") {
				done <- fmt.Errorf("command %d = %q, want STAT", i, line)
				return
			}
		}
		for _, response := range responses {
			if _, err := io.WriteString(server, response); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	return done
}

func readStatCommands(reader *bufio.Reader, commands int) error {
	for i := range commands {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if !strings.HasPrefix(line, "STAT <") {
			return fmt.Errorf("command %d = %q, want STAT", i, line)
		}
	}
	return nil
}

func writeStatResponses(writer io.Writer, messageIDs []string) error {
	for _, messageID := range messageIDs {
		if _, err := fmt.Fprintf(writer, "223 0 <%s>\r\n", messageID); err != nil {
			return err
		}
	}
	return nil
}

func waitForQueuedWorkload(t *testing.T, client *Client, workload Workload) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		client.waitMu.Lock()
		queued := client.waiters[workload].len
		client.waitMu.Unlock()
		if queued > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s work did not enter the admission queue", workload)
}
