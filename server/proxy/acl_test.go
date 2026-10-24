package proxy

import (
	"testing"
	"time"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/ipreg"
)

func TestRegisteredOnlyAppliesClientAndResourceLevels(t *testing.T) {
	ipreg.Reset()
	t.Cleanup(ipreg.Reset)
	client := &file.Client{Id: 42, RegisteredOnly: true}
	remote := "198.51.100.42:54123"

	if !isRegisteredOnlyBlocked(client, remote, false) {
		t.Fatal("client-level registered-only policy allowed an unregistered visitor")
	}
	registeredIP, ok := ipreg.ParseIP("198.51.100.42")
	if !ok {
		t.Fatal("invalid test IP")
	}
	ipreg.Register(client.Id, registeredIP, time.Hour)
	if isRegisteredOnlyBlocked(client, remote, false) {
		t.Fatal("client-level registered-only policy denied a registered visitor")
	}

	client.RegisteredOnly = false
	ipreg.Reset()
	if !isRegisteredOnlyBlocked(client, remote, true) {
		t.Fatal("resource-level registered-only policy allowed an unregistered visitor")
	}
	if isRegisteredOnlyBlocked(client, remote, false) {
		t.Fatal("disabled registered-only policy denied a visitor")
	}
}
