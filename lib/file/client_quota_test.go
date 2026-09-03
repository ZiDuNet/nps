package file

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
)

func TestUserMaxClientNumDefaultsToUnlimitedForExistingJSON(t *testing.T) {
	var user User
	if err := json.Unmarshal([]byte(`{"Id":7,"UserName":"legacy","Password":"secret","Status":true}`), &user); err != nil {
		t.Fatal(err)
	}
	if user.MaxClientNum != 0 {
		t.Fatalf("legacy user max client num = %d, want zero/unlimited", user.MaxClientNum)
	}
}

func TestNewClientEnforcesUserMaxClientNum(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	user := &User{Id: 1, UserName: "alice", Password: "secret", Status: true, MaxClientNum: 2}
	db.Users.Store(user.Id, user)

	for i := 0; i < 2; i++ {
		client := NewClient("", false, false)
		client.UserId = user.Id
		if err := utils.NewClient(client); err != nil {
			t.Fatalf("create client %d: %v", i+1, err)
		}
	}
	defer stopStoredClientRates(db)

	if got := utils.GetUserClientNum(user.Id); got != 2 {
		t.Fatalf("owned client count = %d, want 2", got)
	}
	if err := utils.NewClient(&Client{UserId: user.Id}); err == nil || !strings.Contains(err.Error(), "配额") {
		t.Fatalf("create beyond max client quota err = %v, want quota error", err)
	}
}

func TestConcurrentNewClientRespectsUserMaxClientNum(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	user := &User{Id: 2, UserName: "concurrent", Password: "secret", Status: true, MaxClientNum: 3}
	db.Users.Store(user.Id, user)
	defer stopStoredClientRates(db)

	const workers = 24
	start := make(chan struct{})
	errs := make(chan error, workers)
	var successes int32
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			<-start
			client := NewClient("", false, false)
			client.UserId = user.Id
			if err := utils.NewClient(client); err != nil {
				errs <- err
				return
			}
			atomic.AddInt32(&successes, 1)
		}()
	}
	close(start)
	group.Wait()
	close(errs)

	if got := atomic.LoadInt32(&successes); got != int32(user.MaxClientNum) {
		t.Fatalf("concurrent creates succeeded = %d, want %d", got, user.MaxClientNum)
	}
	if got := utils.GetUserClientNum(user.Id); got != user.MaxClientNum {
		t.Fatalf("persisted client count = %d, want %d", got, user.MaxClientNum)
	}
	for err := range errs {
		if !strings.Contains(err.Error(), "配额") {
			t.Fatalf("concurrent create returned unexpected error: %v", err)
		}
	}
}

func TestUpdateClientOwnerQuotaExcludesCurrentClient(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	ownerA := &User{Id: 1, UserName: "a", Password: "secret", Status: true, MaxClientNum: 1}
	ownerB := &User{Id: 2, UserName: "b", Password: "secret", Status: true, MaxClientNum: 1}
	db.Users.Store(ownerA.Id, ownerA)
	db.Users.Store(ownerB.Id, ownerB)
	db.Clients.Store(10, &Client{Id: 10, UserId: ownerA.Id, VerifyKey: "owner-a"})
	db.Clients.Store(20, &Client{Id: 20, UserId: ownerB.Id, VerifyKey: "owner-b"})
	defer stopStoredClientRates(db)

	// The current client does not consume an additional slot when it remains
	// with the already-full owner.
	if err := utils.UpdateClient(&Client{Id: 10, UserId: ownerA.Id, VerifyKey: "owner-a"}); err != nil {
		t.Fatalf("same-owner update at quota limit: %v", err)
	}
	if err := utils.ValidateClientOwnerQuota(ownerA.Id, 10); err != nil {
		t.Fatalf("owner quota should exclude the current client: %v", err)
	}

	// Moving client 10 to owner B would make B own two clients and must fail.
	if err := utils.UpdateClient(&Client{Id: 10, UserId: ownerB.Id, VerifyKey: "owner-a"}); err == nil || !strings.Contains(err.Error(), "配额") {
		t.Fatalf("transfer to full owner err = %v, want quota error", err)
	}
}

func TestClientExpiryBoundAndEffectiveExpiry(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	owner := &User{
		Id:         9,
		UserName:   "dated",
		Password:   "secret",
		Status:     true,
		ExpireTime: "2030-08-01 12:00:00",
	}
	db.Users.Store(owner.Id, owner)

	following := NewClient("", false, false)
	following.UserId = owner.Id
	if err := utils.NewClient(following); err != nil {
		t.Fatalf("create following client: %v", err)
	}
	defer stopStoredClientRates(db)
	if following.ExpireTime != "" {
		t.Fatalf("blank client expiry changed to %q", following.ExpireTime)
	}
	if got, err := utils.EffectiveClientExpireTime(following); err != nil || got != owner.ExpireTime {
		t.Fatalf("following effective expiry = %q, %v; want owner expiry %q", got, err, owner.ExpireTime)
	}

	earlier := NewClient("", false, false)
	earlier.UserId = owner.Id
	earlier.ExpireTime = "2030-07-01 12:00"
	if err := utils.NewClient(earlier); err != nil {
		t.Fatalf("create earlier-expiring client: %v", err)
	}
	if earlier.ExpireTime != "2030-07-01 12:00:00" {
		t.Fatalf("client expiry was not normalized: %q", earlier.ExpireTime)
	}
	if got, err := utils.EffectiveClientExpireTime(earlier); err != nil || got != earlier.ExpireTime {
		t.Fatalf("earlier effective expiry = %q, %v; want %q", got, err, earlier.ExpireTime)
	}

	later := &Client{UserId: owner.Id, ExpireTime: "2030-09-01 12:00:00"}
	if err := utils.NewClient(later); err == nil || !strings.Contains(err.Error(), "不能晚于") {
		t.Fatalf("later client expiry err = %v, want owner-bound error", err)
	}
	if err := utils.NewClient(&Client{UserId: owner.Id, ExpireTime: "not-a-date"}); err == nil {
		t.Fatal("invalid client expiry was accepted")
	}

	ownerWithoutExpiry := &User{Id: 10, UserName: "unlimited", Password: "secret", Status: true}
	db.Users.Store(ownerWithoutExpiry.Id, ownerWithoutExpiry)
	independent := NewClient("", false, false)
	independent.UserId = ownerWithoutExpiry.Id
	independent.ExpireTime = "2030-09-01 12:00:00"
	if err := utils.NewClient(independent); err != nil {
		t.Fatalf("client with unlimited owner and independent expiry: %v", err)
	}
	if got, err := utils.EffectiveClientExpireTime(independent); err != nil || got != independent.ExpireTime {
		t.Fatalf("independent effective expiry = %q, %v; want %q", got, err, independent.ExpireTime)
	}
}

func TestUpdateUserRejectsExpiryEarlierThanOwnedClient(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	user := &User{Id: 30, UserName: "bounded", Password: "secret", Status: true}
	client := &Client{Id: 31, UserId: user.Id, VerifyKey: "bounded-client", ExpireTime: "2030-08-01 12:00:00"}
	db.Users.Store(user.Id, user)
	db.Clients.Store(client.Id, client)

	updated := &User{
		Id:           user.Id,
		UserName:     user.UserName,
		Password:     user.Password,
		Status:       user.Status,
		MaxClientNum: user.MaxClientNum,
		ExpireTime:   "2030-07-01 12:00:00",
	}
	if err := utils.UpdateUser(updated); err == nil || !strings.Contains(err.Error(), "客户端") {
		t.Fatalf("shortening user expiry err = %v, want owned-client validation error", err)
	}
}

func TestClientSelfExpiryRejectsActivityAndVerifyKey(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	expired := &Client{
		Id:         41,
		VerifyKey:  "self-expired-client",
		Status:     true,
		ExpireTime: time.Now().Add(-time.Minute).Format(canonicalExpireTimeLayout),
	}
	db.Clients.Store(expired.Id, expired)
	defer stopStoredClientRates(db)

	if utils.IsClientActive(expired) {
		t.Fatal("client with an expired own expiry was reported active")
	}
	if _, err := utils.GetIdByVerifyKey(common.Getverifyval(expired.VerifyKey), "192.0.2.41:1234"); err == nil {
		t.Fatal("client with an expired own expiry authenticated successfully")
	}

	future := &Client{
		Id:         42,
		VerifyKey:  "self-future-client",
		Status:     true,
		ExpireTime: time.Now().Add(time.Hour).Format(canonicalExpireTimeLayout),
	}
	db.Clients.Store(future.Id, future)
	if !utils.IsClientActive(future) {
		t.Fatal("client with a future own expiry was reported inactive")
	}
	if id, err := utils.GetIdByVerifyKey(common.Getverifyval(future.VerifyKey), "192.0.2.42:1234"); err != nil || id != future.Id {
		t.Fatalf("future-expiry client authentication = id %d, err %v; want id %d", id, err, future.Id)
	}
}

func stopStoredClientRates(db *JsonDb) {
	db.Clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		rate := client.Rate
		client.RUnlock()
		if rate != nil {
			rate.Stop()
		}
		return true
	})
}
