package file

import (
	"testing"
	"time"
)

func TestSortClientsByDirectionalRates(t *testing.T) {
	first := NewClient("first", false, false)
	first.Id = 1
	first.Flow.Lock()
	first.Flow.rateSampleAt = time.Now()
	first.Flow.rateIn = 128
	first.Flow.rateOut = 32
	first.Flow.Unlock()

	second := NewClient("second", false, false)
	second.Id = 2
	second.Flow.Lock()
	second.Flow.rateSampleAt = time.Now()
	second.Flow.rateIn = 512
	second.Flow.rateOut = 8
	second.Flow.Unlock()

	clients := []*Client{first, second}
	SortClients(clients, "Rate.UploadRate", "desc")
	if clients[0] != second {
		t.Fatalf("upload-rate sort order = [%d, %d], want client 2 first", clients[0].Id, clients[1].Id)
	}

	SortClients(clients, "Rate.DownloadRate", "desc")
	if clients[0] != first {
		t.Fatalf("download-rate sort order = [%d, %d], want client 1 first", clients[0].Id, clients[1].Id)
	}
}
