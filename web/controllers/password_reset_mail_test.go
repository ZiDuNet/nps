package controllers

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRecoveryEmailUsesSMTPAndRecoverySpecificMessage(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	messages := make(chan string, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(connection)
		fmt.Fprint(connection, "220 localhost SMTP test\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "DATA"):
				fmt.Fprint(connection, "354 Send message\r\n")
				var message strings.Builder
				for {
					part, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if part == ".\r\n" {
						break
					}
					message.WriteString(part)
				}
				messages <- message.String()
				fmt.Fprint(connection, "250 Accepted\r\n")
			case strings.HasPrefix(line, "QUIT"):
				fmt.Fprint(connection, "221 Bye\r\n")
				return
			default:
				fmt.Fprint(connection, "250 localhost\r\n")
			}
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	if err := sendPasswordResetEmail("alice@example.test", "123456", registrationSMTPConfig{Host: host, Port: port, Security: "none", From: "nps@example.test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-messages:
		message, err := mail.ReadMessage(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		subject, _ := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
		body, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
		if subject != "NPS 密码找回验证码" || !strings.Contains(string(body), "123456") || !strings.Contains(string(body), "10 分钟") || strings.Contains(string(body), "注册验证码") {
			t.Fatalf("wrong recovery email content: subject=%q", subject)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP did not receive the recovery message")
	}
}
