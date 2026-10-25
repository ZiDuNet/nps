package file

import "testing"

func TestGlobalStateUsesDefensiveSnapshots(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	input := &Glob{ServerUrl: "https://nps.example", BlackIpList: []string{"203.0.113.7"}}
	db.setGlobal(input)
	input.BlackIpList[0] = "198.51.100.9"

	snapshot := db.getGlobal()
	if snapshot == nil || snapshot.ServerUrl != "https://nps.example" || snapshot.BlackIpList[0] != "203.0.113.7" {
		t.Fatalf("global state was not copied on set: %#v", snapshot)
	}
	snapshot.BlackIpList[0] = "192.0.2.1"
	if db.getGlobal().BlackIpList[0] != "203.0.113.7" {
		t.Fatal("global state was exposed as mutable shared data")
	}
}

func TestGlobalIPDeniedSupportsCIDRAndPriority(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.setGlobal(&Glob{
		BlackIpList:    []string{"203.0.113.0/24"},
		WhiteIpList:    []string{"203.0.113.42", "198.51.100.0/24"},
		IpListPriority: 0,
	})
	if !db.GlobalIPDenied("203.0.113.42:1234") {
		t.Fatal("deny-first must reject an address present on both lists")
	}
	if !db.GlobalIPDenied("192.0.2.1:1234") {
		t.Fatal("deny-first with both lists must reject an address outside the denylist")
	}
	db.setGlobal(&Glob{
		BlackIpList:    []string{"203.0.113.0/24"},
		WhiteIpList:    []string{"203.0.113.42"},
		IpListPriority: 1,
	})
	if db.GlobalIPDenied("203.0.113.42:1234") {
		t.Fatal("allow-first must let an allowlist match override the denylist")
	}
	if !db.GlobalIPDenied("203.0.113.43:1234") {
		t.Fatal("allow-first must still reject a denylist-only match")
	}
}

func TestGlobalIPDeniedFailsClosedForInvalidPersistedRules(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.setGlobal(&Glob{BlackIpList: []string{"not-an-ip"}})
	if !db.GlobalIPDenied("198.51.100.7:1234") {
		t.Fatal("invalid persisted global rules should fail closed")
	}
}

func TestGlobalConnectionLimitReservesAndReleasesWithClientSlots(t *testing.T) {
	SetGlobalMaxConn(1)
	t.Cleanup(func() {
		SetGlobalMaxConn(0)
		for {
			active, _ := GlobalConnections()
			if active <= 0 {
				break
			}
			releaseGlobalConn()
		}
	})
	first := NewClient("first", false, false)
	second := NewClient("second", false, false)
	if !first.GetConn() {
		t.Fatal("first client should reserve the global slot")
	}
	if second.GetConn() {
		t.Fatal("second client should be rejected by the global limit")
	}
	first.AddConn()
	if !second.GetConn() {
		t.Fatal("released global slot should be reusable")
	}
	second.AddConn()
	if active, _ := GlobalConnections(); active != 0 {
		t.Fatalf("global active connections = %d, want 0", active)
	}
}
