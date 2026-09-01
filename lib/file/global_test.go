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
