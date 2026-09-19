package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/server/traffic"
)

// requestQueue is intentionally non-blocking. A slow or broken inspection
// stream must never delay the request writer waiting for response metadata.
type requestQueue struct {
	ch chan traffic.Event
}

func newRequestQueue() *requestQueue {
	return &requestQueue{ch: make(chan traffic.Event, 64)}
}

func (q *requestQueue) add(event traffic.Event) {
	if q == nil {
		return
	}
	select {
	case q.ch <- event:
	default:
	}
}

func (q *requestQueue) next() (traffic.Event, bool) {
	if q == nil {
		return traffic.Event{}, false
	}
	select {
	case event := <-q.ch:
		return event, true
	default:
		return traffic.Event{}, false
	}
}

type captureReadCloser struct {
	io.ReadCloser
	capture *traffic.BodyCapture
	once    sync.Once
}

func (c *captureReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if n > 0 {
		c.capture.Write(p[:n])
	}
	if err != nil {
		c.finish()
	}
	return n, err
}

func (c *captureReadCloser) Close() error {
	c.finish()
	return c.ReadCloser.Close()
}

func (c *captureReadCloser) finish() {
	c.once.Do(func() { c.capture.End() })
}

const (
	responseHeaders = iota
	responseFixedBody
	responseChunkSize
	responseChunkBody
	responseChunkCRLF
	responseChunkTrailers
	responseUntilEOF
	responseComplete
)

// responseInspector forwards every byte unchanged while parsing a copy of the
// response framing. It never reads ahead beyond the caller's buffer, and all
// event publishing is best effort through traffic.Publish.
type responseInspector struct {
	reader       io.Reader
	resource     traffic.Resource
	requests     *requestQueue
	streaming    chan struct{}
	streamOnce   sync.Once
	buffer       []byte
	state        int
	remaining    int64
	chunkRemain  int64
	bodyBytes    int64
	body         *traffic.BodyCapture
	request      traffic.Event
	response     traffic.Event
	started      bool
	finishedOnce sync.Once
}

func newResponseInspector(reader io.Reader, resource traffic.Resource, requests *requestQueue, streaming chan struct{}) *responseInspector {
	return &responseInspector{
		reader:    reader,
		resource:  resource,
		requests:  requests,
		streaming: streaming,
		state:     responseHeaders,
	}
}

func (r *responseInspector) Read(p []byte) (int, error) {
	if r == nil || r.reader == nil {
		return 0, io.EOF
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		r.buffer = append(r.buffer, p[:n]...)
		r.inspect()
	}
	if err != nil {
		if err == io.EOF {
			r.finish(nil)
		} else {
			r.finish(err)
		}
	}
	return n, err
}

func (r *responseInspector) inspect() {
	for r.state != responseComplete {
		before := len(r.buffer)
		switch r.state {
		case responseHeaders:
			r.parseHeaders()
		case responseFixedBody:
			r.consumeFixedBody()
		case responseChunkSize:
			r.consumeChunkSize()
		case responseChunkBody:
			r.consumeChunkBody()
		case responseChunkCRLF:
			if len(r.buffer) < 2 {
				return
			}
			if !bytes.Equal(r.buffer[:2], []byte("\r\n")) {
				r.finish(errors.New("invalid chunk terminator"))
				return
			}
			r.buffer = r.buffer[2:]
			r.state = responseChunkSize
		case responseChunkTrailers:
			r.consumeChunkTrailers()
		case responseUntilEOF:
			r.consumeUntilEOF()
		default:
			return
		}
		if len(r.buffer) == before && r.state != responseComplete {
			return
		}
	}
}

func (r *responseInspector) parseHeaders() {
	const maxHeaderBytes = 64 << 10
	end := bytes.Index(r.buffer, []byte("\r\n\r\n"))
	if end < 0 {
		if len(r.buffer) > maxHeaderBytes {
			r.finish(errors.New("response header too large"))
		}
		return
	}
	headerBytes := append([]byte(nil), r.buffer[:end+4]...)
	r.buffer = r.buffer[end+4:]
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(headerBytes)), nil)
	if err != nil {
		r.finish(err)
		return
	}
	defer response.Body.Close()

	if !r.started {
		r.request, r.started = r.requests.next()
	}
	if !r.started {
		r.request = traffic.Event{Resource: r.resource, RequestID: traffic.NewRequestID()}
	}
	r.response = traffic.Event{
		Type:            "response_headers",
		Time:            responseTime(),
		Resource:        r.resource,
		RequestID:       r.request.RequestID,
		Method:          r.request.Method,
		Scheme:          r.request.Scheme,
		Host:            r.request.Host,
		Path:            r.request.Path,
		RemoteAddr:      r.request.RemoteAddr,
		ForwardedFor:    r.request.ForwardedFor,
		ListenerAddr:    r.request.ListenerAddr,
		TargetAddr:      r.request.TargetAddr,
		Status:          response.StatusCode,
		ContentType:     response.Header.Get("Content-Type"),
		ContentEncoding: response.Header.Get("Content-Encoding"),
		Headers:         traffic.SafeHeaders(response.Header),
	}
	watching := traffic.HasSubscribers(r.resource)
	r.body = traffic.NewBodyCapture(r.resource, r.response.RequestID, "response", response.Header.Get("Content-Type"), response.Header.Get("Content-Disposition"))
	if watching {
		r.response.BodySkipped = !r.body.Active() && response.ContentLength != 0
		traffic.Publish(r.response)
	}
	if response.Header.Get("Content-Type") != "" && strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		r.streamOnce.Do(func() {
			if r.streaming != nil {
				close(r.streaming)
			}
		})
	}

	if response.StatusCode >= 100 && response.StatusCode < 200 || response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotModified || strings.EqualFold(r.request.Method, http.MethodHead) {
		if response.StatusCode >= 100 && response.StatusCode < 200 && response.StatusCode != http.StatusSwitchingProtocols {
			r.finishInformationalResponse()
			return
		}
		r.finishResponse(nil)
		return
	}
	if response.TransferEncoding != nil && hasChunkedEncoding(response.TransferEncoding) {
		r.state = responseChunkSize
		return
	}
	if response.ContentLength >= 0 {
		r.remaining = response.ContentLength
		if r.remaining == 0 {
			r.finishResponse(nil)
			return
		}
		r.state = responseFixedBody
		return
	}
	r.state = responseUntilEOF
}

func (r *responseInspector) consumeFixedBody() {
	if r.remaining <= 0 {
		r.finishResponse(nil)
		return
	}
	n := int64(len(r.buffer))
	if n > r.remaining {
		n = r.remaining
	}
	if n > 0 {
		r.body.Write(r.buffer[:n])
		r.bodyBytes += n
		r.buffer = r.buffer[n:]
		r.remaining -= n
	}
	if r.remaining == 0 {
		r.finishResponse(nil)
	}
}

func (r *responseInspector) consumeChunkSize() {
	end := bytes.Index(r.buffer, []byte("\r\n"))
	if end < 0 {
		return
	}
	line := strings.TrimSpace(string(r.buffer[:end]))
	if semi := strings.IndexByte(line, ';'); semi >= 0 {
		line = line[:semi]
	}
	size, err := strconv.ParseInt(strings.TrimSpace(line), 16, 64)
	if err != nil || size < 0 {
		r.finish(fmt.Errorf("invalid response chunk size"))
		return
	}
	r.buffer = r.buffer[end+2:]
	if size == 0 {
		r.state = responseChunkTrailers
		return
	}
	r.chunkRemain = size
	r.state = responseChunkBody
}

func (r *responseInspector) consumeChunkBody() {
	if r.chunkRemain <= 0 {
		r.state = responseChunkCRLF
		return
	}
	n := int64(len(r.buffer))
	if n > r.chunkRemain {
		n = r.chunkRemain
	}
	if n > 0 {
		r.body.Write(r.buffer[:n])
		r.bodyBytes += n
		r.buffer = r.buffer[n:]
		r.chunkRemain -= n
	}
	if r.chunkRemain == 0 {
		r.state = responseChunkCRLF
	}
}

func (r *responseInspector) consumeChunkTrailers() {
	if bytes.HasPrefix(r.buffer, []byte("\r\n")) {
		r.buffer = r.buffer[2:]
		r.finishResponse(nil)
		return
	}
	end := bytes.Index(r.buffer, []byte("\r\n\r\n"))
	if end < 0 {
		return
	}
	r.buffer = r.buffer[end+4:]
	r.finishResponse(nil)
}

func (r *responseInspector) consumeUntilEOF() {
	if len(r.buffer) > 0 {
		r.body.Write(r.buffer)
		r.bodyBytes += int64(len(r.buffer))
		r.buffer = nil
	}
}

func (r *responseInspector) finishResponse(err error) {
	if r.state == responseComplete {
		return
	}
	r.body.End()
	if traffic.HasSubscribers(r.resource) {
		event := r.response
		event.Type = "response_end"
		event.Time = responseTime()
		event.BytesOut = r.bodyBytes
		if !r.request.Time.IsZero() {
			event.DurationMS = time.Since(r.request.Time).Milliseconds()
		}
		event.Complete = err == nil
		if err != nil {
			event.Error = err.Error()
		}
		traffic.Publish(event)
	}
	r.state = responseHeaders
	r.body = nil
	r.response = traffic.Event{}
	r.request = traffic.Event{}
	r.started = false
	r.bodyBytes = 0
}

func (r *responseInspector) finishInformationalResponse() {
	r.body.End()
	if traffic.HasSubscribers(r.resource) {
		event := r.response
		event.Type = "response_end"
		event.Time = responseTime()
		event.Complete = true
		traffic.Publish(event)
	}
	r.state = responseHeaders
	r.body = nil
	r.response = traffic.Event{}
}

func (r *responseInspector) finish(err error) {
	r.finishedOnce.Do(func() {
		if r.state != responseHeaders && r.state != responseComplete {
			r.finishResponse(err)
		}
		r.state = responseComplete
	})
}

func responseTime() (t time.Time) { return time.Now().UTC() }

func hasChunkedEncoding(values []string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), "chunked") {
			return true
		}
	}
	return false
}
