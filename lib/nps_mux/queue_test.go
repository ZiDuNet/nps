package nps_mux

import (
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

func TestPriorityQueueConcurrentProducers(t *testing.T) {
	var queue priorityQueue
	queue.New()

	const producers = 12
	const perProducer = 200
	const total = producers * perProducer
	start := make(chan struct{})
	var producerWG sync.WaitGroup
	producerWG.Add(producers)
	for i := 0; i < producers; i++ {
		go func() {
			defer producerWG.Done()
			<-start
			for j := 0; j < perProducer; j++ {
				queue.Push(&muxPackager{flag: muxNewConn})
			}
		}()
	}

	consumed := make(chan int, 1)
	go func() {
		count := 0
		for count < total {
			if queue.Pop() != nil {
				count++
			}
		}
		consumed <- count
	}()
	close(start)
	producerWG.Wait()

	select {
	case count := <-consumed:
		if count != total {
			t.Fatalf("consumed %d packages, want %d", count, total)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("priority queue did not drain concurrent producers")
	}
	queue.Stop()
}

func TestReceiveWindowQueueTimeoutDoesNotBlockNextPush(t *testing.T) {
	queue := newReceiveWindowQueue()
	queue.SetTimeOut(time.Now().Add(-time.Millisecond))
	if _, err := queue.Pop(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Pop error = %v, want deadline exceeded", err)
	}

	element := &listElement{Buf: []byte("x"), L: 1}
	done := make(chan struct{})
	go func() {
		queue.Push(element)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Push blocked after a timed-out Pop")
	}
	if got, err := queue.Pop(); err != nil || got != element {
		t.Fatalf("Pop after timeout = %p, %v; want %p, nil", got, err, element)
	}
}

func TestReceiveWindowQueueStopUnblocksPopAndIsIdempotent(t *testing.T) {
	queue := newReceiveWindowQueue()
	result := make(chan error, 1)
	go func() {
		_, err := queue.Pop()
		result <- err
	}()

	queue.Stop()
	queue.Stop()
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Pop after Stop error = %v, want EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Pop remained blocked after Stop")
	}

	// A rejected element must not be retained or make a later Pop appear
	// successful. Use a non-pooled buffer to exercise the safe discard path.
	queue.Push(&listElement{Buf: []byte("discard"), L: 7})
	if queue.Len() != 0 {
		t.Fatalf("Len after Push on stopped queue = %d, want 0", queue.Len())
	}
	if element, err := queue.Pop(); !errors.Is(err, io.EOF) || element != nil {
		t.Fatalf("second Pop after Stop = %p, %v; want nil, EOF", element, err)
	}
}

func TestReceiveWindowQueueConcurrentWakeups(t *testing.T) {
	const iterations = 200
	for i := 0; i < iterations; i++ {
		queue := newReceiveWindowQueue()
		queue.SetTimeOut(time.Now().Add(time.Second))
		element := &listElement{Buf: []byte{'x'}, L: 1}
		result := make(chan *listElement, 1)
		errs := make(chan error, 1)
		go func() {
			got, err := queue.Pop()
			result <- got
			errs <- err
		}()
		// Push immediately after starting the reader. This exercises both
		// sides of the check-then-wait transition without relying on sleeps.
		queue.Push(element)
		select {
		case got := <-result:
			if got != element {
				t.Fatalf("iteration %d: Pop element = %p, want %p", i, got, element)
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: Pop did not observe pushed element", i)
		}
		if err := <-errs; err != nil {
			t.Fatalf("iteration %d: Pop error = %v", i, err)
		}
		queue.Stop()
	}
}
