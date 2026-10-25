package bridge

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"ehang.io/nps/lib/conn"
)

func testP2PServiceForBudget() *p2pService {
	return &p2pService{
		nodes:    make(map[string]*p2pBucket),
		clients:  make(map[int]*p2pBucket),
		inflight: make(map[int]int),
	}
}

func testP2PServiceForInvites() *p2pService {
	ctx, cancel := context.WithCancel(context.Background())
	return &p2pService{
		ctx:      ctx,
		cancel:   cancel,
		invites:  make(map[string]*p2pInvite),
		nodes:    make(map[string]*p2pBucket),
		clients:  make(map[int]*p2pBucket),
		inflight: make(map[int]int),
	}
}

func TestP2PAdmissionDoesNotAllocateBeforeClientBudget(t *testing.T) {
	p := testP2PServiceForBudget()
	const clientID = 42

	accepted := 0
	for i := 0; i < 200; i++ {
		if p.admit(clientID, "rotating-node-"+time.Duration(i).String()) {
			accepted++
			p.done(clientID)
		}
	}

	if accepted > int(p2pClientBurst) {
		t.Fatalf("accepted %d requests, want at most the client burst %d", accepted, int(p2pClientBurst))
	}
	if got := len(p.nodes); got > accepted {
		t.Fatalf("allocated %d node buckets for %d accepted requests", got, accepted)
	}
	if got := len(p.nodes); got >= 200 {
		t.Fatalf("rotating node UUIDs grew the map without a budget: %d buckets", got)
	}
}

func TestP2PAdmissionSweepsIdleBuckets(t *testing.T) {
	p := testP2PServiceForBudget()
	now := time.Now().Add(-p2pBudgetIdleAfter - time.Second)
	p.nodes["1/old"] = &p2pBucket{tokens: 1, at: now}
	p.clients[1] = &p2pBucket{tokens: 1, at: now}
	p.lastBudgetSweep = time.Time{}

	if !p.admit(2, "fresh") {
		t.Fatal("fresh admission unexpectedly rejected")
	}
	p.done(2)
	if _, ok := p.nodes["1/old"]; ok {
		t.Fatal("idle node bucket was not swept")
	}
	if _, ok := p.clients[1]; ok {
		t.Fatal("idle client bucket was not swept")
	}
}

func TestP2PAdmissionHonorsBucketCaps(t *testing.T) {
	p := testP2PServiceForBudget()
	for i := 0; i < p2pMaxNodeBuckets; i++ {
		p.nodes["1/node-"+time.Duration(i).String()] = &p2pBucket{tokens: 1, at: time.Now()}
	}
	if p.admit(1, "new-node") {
		t.Fatal("admission succeeded after the node bucket cap was reached")
	}
}

func TestP2PInviteRejectsAfterServiceClose(t *testing.T) {
	p := testP2PServiceForInvites()
	p.close()

	_, _, _, _, err := p.invite(7)
	if !errors.Is(err, errP2PServiceClosed) {
		t.Fatalf("invite error = %v, want %v", err, errP2PServiceClosed)
	}
}

func TestP2PServiceCloseClosesQueuedJoin(t *testing.T) {
	p := testP2PServiceForInvites()
	id, token, _, drop, err := p.invite(7)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	defer drop()

	server, peer := net.Pipe()
	defer peer.Close()
	if err := p.claim(id, 7, token, p2pJoined{c: conn.NewConn(server)}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	p.close()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := peer.Read(one[:]); err == nil {
		t.Fatal("queued provider stream remained open after service close")
	}
}

func TestP2PClaimRejectsAfterServiceCloseWithoutQueueing(t *testing.T) {
	p := testP2PServiceForInvites()
	id, token, _, drop, err := p.invite(7)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	defer drop()
	p.close()

	server, peer := net.Pipe()
	defer peer.Close()
	if err := p.claim(id, 7, token, p2pJoined{c: conn.NewConn(server)}); !errors.Is(err, errP2PServiceClosed) {
		t.Fatalf("claim error = %v, want %v", err, errP2PServiceClosed)
	}
	_ = peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	var one [1]byte
	if _, err := peer.Read(one[:]); err == nil {
		t.Fatal("claim unexpectedly queued a stream after service close")
	}
	closeP2PConn(conn.NewConn(server))
}
