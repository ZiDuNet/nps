//go:build !npcgui

package server

import (
	"net"
	"testing"
	"time"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/server/proxy"
)

func TestEditRunningUDPTunnelBindsReplacementBeforeReturning(t *testing.T) {
	previousDB, previousBridge := file.GetDb(), Bridge
	db := &file.DbUtils{JsonDb: file.NewJsonDb(t.TempDir())}
	file.Db, Bridge = db, &bridge.Bridge{}
	t.Cleanup(func() { file.Db, Bridge = previousDB, previousBridge })
	clientA := &file.Client{Id: 11, Status: true, Cnf: &file.Config{}, Flow: file.NewFlow()}
	clientB := &file.Client{Id: 22, Status: true, Cnf: &file.Config{}, Flow: file.NewFlow()}
	db.JsonDb.Clients.Store(clientA.Id, clientA)
	db.JsonDb.Clients.Store(clientB.Id, clientB)
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
	reservation, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	addr = reservation.LocalAddr().(*net.UDPAddr)
	_ = reservation.Close()
	task := &file.Tunnel{
		Id: 73, Mode: "udp", ServerIp: "127.0.0.1", Port: addr.Port, Status: true,
		Client: clientA, Target: &file.Target{TargetStr: "127.0.0.1:12345"}, Flow: file.NewFlow(),
	}
	db.JsonDb.Tasks.Store(task.Id, task)
	if err := AddTask(task); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if current, ok := RunList.LoadAndDelete(task.Id); ok {
			_ = current.(proxy.Service).Close()
		}
	})
	previous, _ := RunList.Load(task.Id)
	replacement := &file.Tunnel{
		Id: task.Id, Mode: task.Mode, ServerIp: task.ServerIp, Port: task.Port,
		Client: clientB, Target: task.Target, Flow: file.NewFlow(),
	}
	if saved, err := EditTask(task, replacement, clientA, true); !saved || err != nil {
		t.Fatalf("edit returned saved=%t, err=%v", saved, err)
	}
	if current, exists := RunList.Load(task.Id); !exists || current == previous || !task.Status || task.Client != clientB {
		t.Fatal("UDP did not replace its listener and switch to the new client")
	}
	// A successful edit means the real listener already owns the port, not
	// that a background goroutine is about to try binding it.
	if conflicting, err := net.ListenUDP("udp", addr); err == nil {
		_ = conflicting.Close()
		t.Fatal("UDP port was not bound when edit returned")
	}
}

func TestEditRunningTCPTunnelKeepsListenerAndSelectsNewClient(t *testing.T) {
	previous := file.GetDb()
	db := &file.DbUtils{JsonDb: file.NewJsonDb(t.TempDir())}
	file.Db = db
	t.Cleanup(func() { file.Db = previous })
	clientA := &file.Client{Id: 11, Status: true}
	clientB := &file.Client{Id: 22, Status: true}
	db.JsonDb.Clients.Store(clientA.Id, clientA)
	db.JsonDb.Clients.Store(clientB.Id, clientB)
	task := &file.Tunnel{
		Id: 73, Mode: "tcp", ServerIp: "127.0.0.1", Status: true, Client: clientA,
		Target: &file.Target{TargetStr: "127.0.0.1:80"}, Flow: file.NewFlow(),
	}
	db.JsonDb.Tasks.Store(task.Id, task)
	listener := proxy.NewTunnelModeServer(func(c *conn.Conn, _ *proxy.TunnelModeServer) error {
		defer c.Close()
		owner := byte(tunnelClientID(task))
		var data [1]byte
		for {
			if _, err := c.Read(data[:]); err != nil {
				return err
			}
			if _, err := c.Write([]byte{owner}); err != nil {
				return err
			}
		}
	}, nil, task)
	if err := listener.StartListening(); err != nil {
		t.Fatal(err)
	}
	RunList.Store(task.Id, listener)
	t.Cleanup(func() {
		_ = listener.Close()
		RunList.Delete(task.Id)
	})

	oldConn, err := listener.DialVirtual("198.51.100.5:23456")
	if err != nil {
		t.Fatal(err)
	}
	defer oldConn.Close()
	checkOwner := func(c net.Conn, id int) {
		t.Helper()
		_ = c.SetDeadline(time.Now().Add(time.Second))
		if _, err := c.Write([]byte{'?'}); err != nil {
			t.Fatal(err)
		}
		var answer [1]byte
		if _, err := c.Read(answer[:]); err != nil || int(answer[0]) != id {
			t.Fatalf("connection owner = %d, err = %v; want %d", answer[0], err, id)
		}
	}
	checkOwner(oldConn, clientA.Id)
	replacement := &file.Tunnel{
		Id: task.Id, Mode: task.Mode, ServerIp: task.ServerIp, Port: task.Port,
		Client: clientB, Target: task.Target, Flow: file.NewFlow(),
	}
	if saved, err := EditTask(task, replacement, clientA, true); !saved || err != nil {
		t.Fatalf("edit returned saved=%t, err=%v", saved, err)
	}
	if live, exists := RunList.Load(task.Id); !exists || live != listener || !task.Status {
		t.Fatal("owner edit did not keep the enabled listener")
	}
	checkOwner(oldConn, clientA.Id)
	newConn, err := listener.DialVirtual("198.51.100.5:23457")
	if err != nil {
		t.Fatal(err)
	}
	defer newConn.Close()
	checkOwner(newConn, clientB.Id)
}
