package server

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"testing"

	"github.com/astaxie/beego"
)

func TestStartIPRegistrationServerUsesOptionalDedicatedListener(t *testing.T) {
	oldValues := map[string]string{
		"ip_register_ip":        beego.AppConfig.String("ip_register_ip"),
		"ip_register_port":      beego.AppConfig.String("ip_register_port"),
		"ip_register_cert_file": beego.AppConfig.String("ip_register_cert_file"),
		"ip_register_key_file":  beego.AppConfig.String("ip_register_key_file"),
	}
	t.Cleanup(func() {
		for key, value := range oldValues {
			_ = beego.AppConfig.Set(key, value)
		}
	})

	_ = beego.AppConfig.Set("ip_register_port", "0")
	if srv, err := StartIPRegistrationServer(nil); err != nil || srv != nil {
		t.Fatalf("disabled listener = (%v, %v), want (nil, nil)", srv, err)
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a test port: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	_ = beego.AppConfig.Set("ip_register_ip", "127.0.0.1")
	_ = beego.AppConfig.Set("ip_register_port", strconv.Itoa(port))
	_ = beego.AppConfig.Set("ip_register_cert_file", "")
	_ = beego.AppConfig.Set("ip_register_key_file", "")

	srv, err := StartIPRegistrationServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("registration-only"))
	}))
	if err != nil {
		t.Fatalf("start dedicated listener: %v", err)
	}
	if srv == nil {
		t.Fatal("configured listener was not started")
	}
	t.Cleanup(func() { _ = srv.Close() })

	response, err := http.Get("http://" + srv.listener.Addr().String() + "/")
	if err != nil {
		t.Fatalf("request registration listener: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("registration listener status = %d", response.StatusCode)
	}
	if response.Request.URL.Host != fmt.Sprintf("127.0.0.1:%d", port) {
		t.Fatalf("request host = %q", response.Request.URL.Host)
	}
}
