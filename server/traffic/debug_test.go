package traffic

import (
	"net/http"
	"testing"
	"time"
)

func TestPublishIsNonBlockingAndReportsOverflow(t *testing.T) {
	resource := Resource{Kind: "test", ID: time.Now().Nanosecond()}
	events, closeStream := Subscribe(resource)
	defer closeStream()

	for i := 0; i < subscriberBuffer+20; i++ {
		Publish(Event{Type: "test", Resource: resource})
	}

	seenOverflow := false
	for {
		select {
		case event := <-events:
			if event.Type == "dropped" {
				seenOverflow = true
			}
		default:
			if !seenOverflow {
				Publish(Event{Type: "wake", Resource: resource})
				for {
					select {
					case event := <-events:
						if event.Type == "dropped" {
							seenOverflow = true
						}
					default:
						if !seenOverflow {
							t.Fatal("expected an overflow event")
						}
						return
					}
				}
			}
			return
		}
	}
}

func TestBodyCapturePolicyAndCompleteHeaders(t *testing.T) {
	if !IsInspectableContentType("application/json", "") {
		t.Fatal("JSON should be inspectable")
	}
	for _, contentType := range []string{"application/zip", "image/png", "application/octet-stream", "multipart/form-data"} {
		if IsInspectableContentType(contentType, "") {
			t.Fatalf("%s should be excluded", contentType)
		}
	}
	if IsInspectableContentType("text/plain", "attachment; filename=x.txt") {
		t.Fatal("attachments should be excluded")
	}

	header := http.Header{
		"Authorization": {"Bearer secret"},
		"Cookie":        {"session=secret"},
		"X-Trace":       {"trace-id"},
	}
	safe := SafeHeaders(header)
	if safe["Authorization"] != "Bearer secret" || safe["Cookie"] != "session=secret" || safe["X-Trace"] != "trace-id" {
		t.Fatalf("unexpected complete headers: %#v", safe)
	}
}

func TestBodyCaptureEndsOnlyOnce(t *testing.T) {
	resource := Resource{Kind: "test", ID: time.Now().Nanosecond()}
	events, closeStream := Subscribe(resource)
	defer closeStream()
	capture := NewBodyCapture(resource, "request", "response", "application/json", "")
	capture.Write([]byte(`{"ok":true}`))
	capture.End()
	capture.End()

	ends := 0
	for {
		select {
		case event := <-events:
			if event.Type == "body_end" {
				ends++
			}
		default:
			if ends != 1 {
				t.Fatalf("body end events = %d, want 1", ends)
			}
			return
		}
	}
}
