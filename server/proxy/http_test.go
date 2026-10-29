package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"

	"ehang.io/nps/lib/common"
)

func TestWriteConnFailContentKeepsConcurrentBodiesIndependent(t *testing.T) {
	const requests = 12
	server := &BaseServer{}
	type result struct {
		body []byte
		err  error
	}
	results := make(chan result, requests)
	var writers sync.WaitGroup

	for index := 0; index < requests; index++ {
		client, peer := net.Pipe()
		body := []byte(fmt.Sprintf("request-body-%d", index))
		writers.Add(1)
		go func(c net.Conn, expected []byte) {
			defer writers.Done()
			defer c.Close()
			server.writeConnFailContent(c, expected)
		}(client, body)
		go func(p net.Conn, expected []byte) {
			defer p.Close()
			actual, err := io.ReadAll(p)
			want := append(append([]byte{}, common.ConnectionFailBytes...), expected...)
			if err == nil && !bytes.Equal(actual, want) {
				err = fmt.Errorf("got %q, want %q", actual, want)
			}
			results <- result{body: expected, err: err}
		}(peer, body)
	}

	writers.Wait()
	for index := 0; index < requests; index++ {
		item := <-results
		if item.err != nil {
			t.Fatalf("concurrent failure response %q: %v", item.body, item.err)
		}
	}
}

func TestHTTPServerConfiguresHeaderAndIdleTimeouts(t *testing.T) {
	server := (&httpServer{}).NewServer(8080, "http")
	if server.ReadHeaderTimeout != httpReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %s, want %s", server.ReadHeaderTimeout, httpReadHeaderTimeout)
	}
	if server.IdleTimeout != httpIdleTimeout {
		t.Fatalf("IdleTimeout = %s, want %s", server.IdleTimeout, httpIdleTimeout)
	}
	managed := newManagedHTTPServer(http.NotFoundHandler())
	if managed.ReadHeaderTimeout != httpReadHeaderTimeout || managed.IdleTimeout != httpIdleTimeout {
		t.Fatalf("managed server timeouts = (%s, %s), want (%s, %s)", managed.ReadHeaderTimeout, managed.IdleTimeout, httpReadHeaderTimeout, httpIdleTimeout)
	}
}

func TestHTTPServerCloseBeforeStartPreventsLateBind(t *testing.T) {
	server := NewHttp(nil, nil, 0, 0, false, 0, false)
	if err := server.Close(); err != nil {
		t.Fatalf("close before start: %v", err)
	}
	if err := server.Start(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("start after close = %v, want %v", err, net.ErrClosed)
	}
}

func TestHTTPServerStartCloseRaceIsSafe(t *testing.T) {
	server := NewHttp(nil, nil, 0, 0, false, 0, false)
	startDone := make(chan error, 1)
	closeDone := make(chan error, 1)
	go func() { startDone <- server.Start() }()
	go func() { closeDone <- server.Close() }()
	if err := <-startDone; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("start during close: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("close during start: %v", err)
	}
}
