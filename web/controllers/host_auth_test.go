package controllers

import (
	"encoding/json"
	"strings"
	"testing"

	"ehang.io/nps/lib/file"
)

func TestHostListExposesAuthStatusWithoutCredentials(t *testing.T) {
	host := &file.Host{
		Id:     12,
		Host:   "private.example.test",
		Auth:   "alice:do-not-leak-this\nbob:also-secret",
		Client: file.NewClient("test-vkey", false, false),
		Target: &file.Target{TargetStr: "127.0.0.1:8080"},
	}
	rows := newHostListRows([]*file.Host{host})
	if len(rows) != 1 || !rows[0].BasicAuthSet {
		t.Fatalf("host list auth status = %#v, want configured", rows)
	}
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatalf("marshal host row: %v", err)
	}
	for _, secret := range []string{"do-not-leak-this", "also-secret", "alice", "bob", `"Auth"`} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("host list JSON leaked %q: %s", secret, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"BasicAuthSet":true`) {
		t.Fatalf("host list JSON did not include BasicAuthSet: %s", encoded)
	}
}

func TestHostListExposesClientLevelAuthStatusWithoutCredentials(t *testing.T) {
	client := file.NewClient("test-vkey", false, false)
	client.Cnf.U = "client-user"
	client.Cnf.P = "client-secret"
	host := &file.Host{
		Id:     13,
		Host:   "client-auth.example.test",
		Auth:   "",
		Client: client,
		Target: &file.Target{TargetStr: "127.0.0.1:8080"},
	}
	rows := newHostListRows([]*file.Host{host})
	if len(rows) != 1 || rows[0].BasicAuthSet || !rows[0].Client.BasicAuthSet {
		t.Fatalf("client-level auth status = %#v, want client configured only", rows)
	}
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatalf("marshal host row: %v", err)
	}
	for _, secret := range []string{"client-user", "client-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("host list JSON leaked %q: %s", secret, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"BasicAuthSet":true`) {
		t.Fatalf("host list JSON did not include client-level BasicAuthSet: %s", encoded)
	}
}
