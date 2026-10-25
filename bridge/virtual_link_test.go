package bridge

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"ehang.io/nps/server/tool"
)

func TestDialBridgeTargetUsesVirtualHandlers(t *testing.T) {
	webListener := tool.NewVirtualListener(nil)
	ipregListener := tool.NewVirtualListener(nil)
	t.Cleanup(func() {
		_ = webListener.Close()
		_ = ipregListener.Close()
		tool.SetWebServerListener(nil)
		tool.SetIPRegisterListener(nil)
	})
	tool.SetWebServerListener(webListener)
	tool.SetIPRegisterListener(ipregListener)

	server := &Bridge{}
	server.virtualTCP = tool.NewVirtualListener(nil)
	server.virtualTLS = tool.NewVirtualListener(nil)
	server.virtualWS = tool.NewVirtualListener(nil)
	server.virtualWSS = tool.NewVirtualListener(nil)
	t.Cleanup(server.closeVirtualBridgeListeners)

	listeners := map[string]*tool.VirtualListener{
		"tcp":   server.virtualTCP,
		"tls":   server.virtualTLS,
		"ws":    server.virtualWS,
		"wss":   server.virtualWSS,
		"web":   webListener,
		"ipreg": ipregListener,
	}
	for target, listener := range listeners {
		t.Run(target, func(t *testing.T) {
			accepted := make(chan net.Conn, 1)
			acceptErr := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					acceptErr <- err
					return
				}
				accepted <- conn
			}()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			client, err := server.dialBridgeTarget(target, "198.51.100.10:44321")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()

			var backend net.Conn
			select {
			case backend = <-accepted:
			case err := <-acceptErr:
				t.Fatalf("accept %s target: %v", target, err)
			case <-ctx.Done():
				t.Fatalf("timed out accepting %s target", target)
			}
			defer backend.Close()
			if got, want := backend.RemoteAddr().String(), "198.51.100.10:44321"; got != want {
				t.Fatalf("remote address = %q, want %q", got, want)
			}

			request := make([]byte, 4)
			readRequest := make(chan error, 1)
			go func() {
				_, err := io.ReadFull(backend, request)
				readRequest <- err
			}()
			if _, err := client.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			if err := <-readRequest; err != nil {
				t.Fatal(err)
			}
			if string(request) != "ping" {
				t.Fatalf("request = %q, want ping", request)
			}
			response := make([]byte, 4)
			readResponse := make(chan error, 1)
			go func() {
				_, err := io.ReadFull(client, response)
				readResponse <- err
			}()
			if _, err := backend.Write([]byte("pong")); err != nil {
				t.Fatal(err)
			}
			if err := <-readResponse; err != nil {
				t.Fatal(err)
			}
			if string(response) != "pong" {
				t.Fatalf("response = %q, want pong", response)
			}
		})
	}
}

func TestNestedTunnelTenantBoundary(t *testing.T) {
	for _, test := range []struct {
		name              string
		sourceClientID    int
		sourceUserID      int
		destinationClient int
		destinationUserID int
		want              bool
	}{
		{name: "same tenant may chain clients", sourceClientID: 1, sourceUserID: 10, destinationClient: 2, destinationUserID: 10, want: true},
		{name: "different tenant is rejected", sourceClientID: 1, sourceUserID: 10, destinationClient: 2, destinationUserID: 11},
		{name: "admin-owned route remains in the same client", sourceClientID: 1, destinationClient: 1, want: true},
		{name: "shared admin client cannot reach another admin client", sourceClientID: 1, destinationClient: 2},
		{name: "admin-owned route cannot enter user tenant", sourceClientID: 1, destinationClient: 2, destinationUserID: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := nestedTunnelTenantAllowed(test.sourceClientID, test.sourceUserID, test.destinationClient, test.destinationUserID); got != test.want {
				t.Fatalf("nestedTunnelTenantAllowed() = %t, want %t", got, test.want)
			}
		})
	}
}
