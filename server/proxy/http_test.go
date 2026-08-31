package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net"
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
