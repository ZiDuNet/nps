package file

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
)

// ParseBasicAuth parses newline-separated username:password pairs. The first
// colon separates the username from the password, so colons remain valid in a
// password. Write paths reject malformed or duplicate accounts.
func ParseBasicAuth(value string) (map[string]string, error) {
	accounts := make(map[string]string)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, errors.New("Basic 认证必须按每行 用户名:密码 填写")
		}
		username := strings.TrimSpace(parts[0])
		password := strings.TrimSpace(parts[1])
		if username == "" || password == "" {
			return nil, errors.New("Basic 认证用户名和密码不能为空")
		}
		if strings.ContainsAny(username, "\r\n:") || strings.ContainsAny(password, "\r\n") {
			return nil, errors.New("Basic 认证不能包含换行或非法分隔符")
		}
		if _, exists := accounts[username]; exists {
			return nil, errors.New("Basic 认证用户名不能重复")
		}
		accounts[username] = password
	}
	return accounts, nil
}

func BasicAuthConfigured(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	accounts, err := ParseBasicAuth(value)
	return err == nil && len(accounts) > 0
}

// CheckBasicAuth accepts the normal Authorization header and the legacy
// Proxy-Authorization header so host rules work for reverse and forward
// proxy callers alike.
func CheckBasicAuth(r *http.Request, value string) bool {
	if r == nil || !BasicAuthConfigured(value) {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		header = strings.TrimSpace(r.Header.Get("Proxy-Authorization"))
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Basic") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
	if err != nil {
		return false
	}
	credentials := strings.SplitN(string(decoded), ":", 2)
	if len(credentials) != 2 {
		return false
	}
	accounts, err := ParseBasicAuth(value)
	if err != nil {
		return false
	}
	password, ok := accounts[credentials[0]]
	return ok && password == credentials[1]
}
