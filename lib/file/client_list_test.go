package file

import "testing"

func TestGetClientListForAllowedIdsFiltersBeforePagination(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	clients := []*Client{
		{Id: 1, UserId: 7, VerifyKey: "client-one", Remark: "one"},
		{Id: 2, UserId: 9, VerifyKey: "client-two", Remark: "two"},
		{Id: 3, UserId: 7, VerifyKey: "client-three", Remark: "three"},
		{Id: 4, UserId: 9, VerifyKey: "client-four", Remark: "four"},
	}
	for _, client := range clients {
		db.Clients.Store(client.Id, client)
	}

	utils := &DbUtils{JsonDb: db}
	allowed := map[int]struct{}{1: {}, 3: {}}

	firstPage, total := utils.GetClientListForAllowedIds(0, 1, "", "", "", 0, allowed)
	if total != 2 {
		t.Fatalf("filtered total = %d, want 2", total)
	}
	if len(firstPage) != 1 || firstPage[0].Id != 1 {
		t.Fatalf("first filtered page = %#v, want client 1", clientIDs(firstPage))
	}

	secondPage, total := utils.GetClientListForAllowedIds(1, 1, "", "", "", 0, allowed)
	if total != 2 {
		t.Fatalf("filtered total on second page = %d, want 2", total)
	}
	if len(secondPage) != 1 || secondPage[0].Id != 3 {
		t.Fatalf("second filtered page = %#v, want client 3", clientIDs(secondPage))
	}
}

func TestGetClientListFilteredByOwnerAndRemark(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.Clients.Store(1, &Client{Id: 1, UserId: 7, VerifyKey: "alpha", Remark: "production"})
	db.Clients.Store(2, &Client{Id: 2, UserId: 7, VerifyKey: "beta", Remark: "staging"})
	db.Clients.Store(3, &Client{Id: 3, UserId: 9, VerifyKey: "gamma", Remark: "production"})
	db.Clients.Store(4, &Client{Id: 4, UserId: 0, VerifyKey: "delta", Remark: "unassigned"})
	utils := &DbUtils{JsonDb: db}

	owner := 7
	clients, total := utils.GetClientListFiltered(0, 20, "stag", "", "", 0, &OwnerFilter{UserID: &owner}, nil)
	if total != 1 || len(clients) != 1 || clients[0].Id != 2 {
		t.Fatalf("owner + remark filter = ids=%v total=%d, want [2], 1", clientIDs(clients), total)
	}

	unassigned := 0
	clients, total = utils.GetClientListFiltered(0, 20, "", "", "", 0, &OwnerFilter{UserID: &unassigned}, nil)
	if total != 1 || len(clients) != 1 || clients[0].Id != 4 {
		t.Fatalf("unassigned filter = ids=%v total=%d, want [4], 1", clientIDs(clients), total)
	}
}

func clientIDs(clients []*Client) []int {
	ids := make([]int, 0, len(clients))
	for _, client := range clients {
		if client != nil {
			ids = append(ids, client.Id)
		}
	}
	return ids
}
