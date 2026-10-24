package bridge

import (
	"testing"

	"ehang.io/nps/lib/file"
)

func TestRegisteredIPRequiredCombinesClientAndResourcePolicy(t *testing.T) {
	client := &file.Client{RegisteredOnly: true}
	if !registeredIPRequired(client, nil) {
		t.Fatal("client-level policy was ignored")
	}
	client.RegisteredOnly = false
	if !registeredIPRequired(client, &file.Tunnel{RegisteredOnly: true}) {
		t.Fatal("resource-level policy was ignored")
	}
	if registeredIPRequired(client, &file.Tunnel{}) {
		t.Fatal("disabled policy unexpectedly required registration")
	}
	if !registeredIPRequired(nil, &file.Tunnel{RegisteredOnly: true}) {
		t.Fatal("resource policy should fail closed when the client record is missing")
	}
}
