package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ehang.io/nps/lib/file"
)

// applyHeaderChanges applies one "Name: Value" rule per line. An empty value
// or a name prefixed with '-' removes a response header. Invalid names and
// values are ignored so a bad optional rule cannot corrupt the proxy stream.
func applyHeaderChanges(headers http.Header, rules string) {
	for _, line := range strings.Split(strings.ReplaceAll(rules, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		nameForProtection := line
		if strings.HasPrefix(nameForProtection, "-") {
			nameForProtection = strings.TrimSpace(nameForProtection[1:])
		} else if name, _, ok := strings.Cut(nameForProtection, ":"); ok {
			nameForProtection = strings.TrimSpace(name)
		}
		if protectedResponseHeader(nameForProtection) {
			continue
		}
		if strings.HasPrefix(line, "-") && strings.TrimSpace(line[1:]) != "" {
			headers.Del(strings.TrimSpace(line[1:]))
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" || strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			continue
		}
		if value == "" {
			headers.Del(name)
		} else {
			headers.Set(name, value)
		}
	}
}

func protectedResponseHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "content-length", "transfer-encoding", "connection":
		return true
	default:
		return false
	}
}

func rewriteHostRequestPath(request *http.Request, rules string) {
	if request == nil || strings.TrimSpace(rules) == "" || request.URL == nil {
		return
	}
	path := request.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	for _, line := range strings.Split(strings.ReplaceAll(rules, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		old, replacement, ok := strings.Cut(line, "=>")
		if !ok {
			old, replacement, ok = strings.Cut(line, "=")
		}
		if !ok {
			continue
		}
		old, replacement = strings.TrimSpace(old), strings.TrimSpace(replacement)
		if old == "" || !strings.HasPrefix(old, "/") || !strings.HasPrefix(replacement, "/") {
			continue
		}
		if path == old || strings.HasPrefix(path, strings.TrimRight(old, "/")+"/") {
			remainder := path[len(old):]
			path = strings.TrimRight(replacement, "/") + remainder
			if path == "" {
				path = "/"
			}
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			request.URL.Path, _ = url.PathUnescape(path)
			request.URL.RawPath = path
			return
		}
	}
}

func redirectLocation(request *http.Request, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return ""
	}
	path := "/"
	if request != nil && request.URL != nil {
		path = request.URL.RequestURI()
	}
	return strings.ReplaceAll(configured, "{path}", path)
}

// responseHeaderTransform forwards response bytes while replacing only the
// first HTTP response header block. Body bytes and chunk framing are untouched.
// It is used by the raw HTTP/1.1 path, where net/http's ModifyResponse hook is
// not available.
type responseHeaderTransform struct {
	reader io.Reader
	host   *file.Host
	buf    []byte
	done   bool
}

func newResponseHeaderTransform(reader io.Reader, host *file.Host) io.Reader {
	return &responseHeaderTransform{reader: reader, host: host}
}

func (t *responseHeaderTransform) Read(p []byte) (int, error) {
	if t.done {
		if len(t.buf) > 0 {
			n := copy(p, t.buf)
			t.buf = t.buf[n:]
			return n, nil
		}
		return t.reader.Read(p)
	}
	for {
		if end := bytes.Index(t.buf, []byte("\r\n\r\n")); end >= 0 {
			headers := append([]byte(nil), t.buf[:end+4]...)
			remainder := append([]byte(nil), t.buf[end+4:]...)
			transformed := transformResponseHeaders(headers, t.host)
			t.buf = append(transformed, remainder...)
			t.done = true
			if len(t.buf) > 0 {
				n := copy(p, t.buf)
				t.buf = t.buf[n:]
				return n, nil
			}
			continue
		}
		var chunk [16 * 1024]byte
		n, err := t.reader.Read(chunk[:])
		if n > 0 {
			t.buf = append(t.buf, chunk[:n]...)
			if len(t.buf) > 64*1024 {
				// Do not buffer an unbounded malformed response. Forward the
				// original bytes and leave later reads untouched.
				copyN := copy(p, t.buf)
				t.buf = t.buf[copyN:]
				t.done = true
				return copyN, nil
			}
			continue
		}
		if err != nil {
			if len(t.buf) > 0 {
				copyN := copy(p, t.buf)
				t.buf = t.buf[copyN:]
				if len(t.buf) == 0 {
					t.done = true
				}
				return copyN, nil
			}
			return 0, err
		}
	}
}

func transformResponseHeaders(raw []byte, host *file.Host) []byte {
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		return raw
	}
	defer response.Body.Close()
	if host != nil {
		host.RLock()
		rules, cors := host.ResponseHeaderChange, host.AutoCORS
		host.RUnlock()
		applyHeaderChanges(response.Header, rules)
		if cors {
			response.Header.Set("Access-Control-Allow-Origin", "*")
			response.Header.Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			response.Header.Set("Access-Control-Allow-Headers", "*")
		}
	}
	var out bytes.Buffer
	statusText := response.Status
	if statusText == "" {
		statusText = strconv.Itoa(response.StatusCode)
	}
	fmt.Fprintf(&out, "HTTP/%d.%d %s\r\n", response.ProtoMajor, response.ProtoMinor, statusText)
	_ = response.Header.Write(&out)
	if len(response.TransferEncoding) > 0 {
		fmt.Fprintf(&out, "Transfer-Encoding: %s\r\n", strings.Join(response.TransferEncoding, ", "))
	}
	out.WriteString("\r\n")
	return out.Bytes()
}
