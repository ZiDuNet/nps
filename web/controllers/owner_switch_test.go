package controllers

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/store"
	"ehang.io/nps/server"
	"ehang.io/nps/server/tool"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

func newOwnerSwitchController(method, action string, form url.Values) (*IndexController, *httptest.ResponseRecorder) {
	path := "http://console.test/index/" + strings.ToLower(action)
	if method == http.MethodGet {
		path += "?" + form.Encode()
	}
	request := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", "http://console.test")
	}
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &IndexController{}
	controller.Init(ctx, "IndexController", action, controller)
	session := &clientPermissionSession{values: map[interface{}]interface{}{"isAdmin": true, "auth": true}}
	controller.CruSession = session
	ctx.Input.CruSession = session
	return controller, recorder
}

func ownerSwitchTestDB(t *testing.T) (*file.DbUtils, *store.Store, *file.Client, *file.Client) {
	t.Helper()
	db := useClientPermissionTestDb(t)
	storage, err := store.Open(filepath.Join(db.JsonDb.RunPath, "conf", "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.JsonDb.SetSQLite(storage)
	t.Cleanup(func() { _ = storage.Close() })
	clientA := &file.Client{Id: 101, Status: true, Cnf: &file.Config{}, Flow: file.NewFlow()}
	clientB := &file.Client{Id: 202, Status: true, Cnf: &file.Config{}, Flow: file.NewFlow()}
	db.JsonDb.Clients.Store(clientA.Id, clientA)
	db.JsonDb.Clients.Store(clientB.Id, clientB)
	return db, storage, clientA, clientB
}

func savedRoute(t *testing.T, storage *store.Store, kind string, id int) (row struct {
	Client   struct{ Id int }
	Status   bool
	RunError string
}) {
	t.Helper()
	values, err := storage.LoadEntities()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(values[kind][id], &row); err != nil {
		t.Fatalf("decode saved %s %d: %v", kind, id, err)
	}
	return row
}

func TestCustomHostEditRendersDomainAndSavesNewClient(t *testing.T) {
	db, storage, oldClient, newClient := ownerSwitchTestDB(t)
	previousBridge := server.Bridge
	server.Bridge = &bridge.Bridge{}
	t.Cleanup(func() { server.Bridge = previousBridge })

	const domain = "owner-check.example.test"
	const auth = "fixture-user:synthetic-auth-value"
	host := &file.Host{
		Id: 1, Host: domain, Scheme: "https", Location: "/", Remark: "fixture host",
		Client: oldClient, Target: &file.Target{TargetStr: "127.0.0.1:12345"}, Flow: file.NewFlow(),
		Auth: auth, CertFilePath: "conf/certs/existing.pem", KeyFilePath: "conf/certs/existing.key",
	}
	db.JsonDb.Hosts.Store(host.Id, host)
	get, _ := newOwnerSwitchController(http.MethodGet, "EditHost", url.Values{"id": {"1"}})
	get.EditHost()
	var rendered bytes.Buffer
	page := template.Must(template.ParseFiles("../views/" + get.TplName))
	if err := page.Execute(&rendered, get.Data); err != nil {
		t.Fatal(err)
	}
	content := rendered.String()
	if !strings.Contains(content, `name="host" value="`+domain+`"`) {
		t.Fatal("custom domain did not round-trip into the edit form")
	}
	for _, unexpected := range []string{"<nil>", "{0 0", "map[", "0x"} {
		if strings.Contains(content, unexpected) {
			t.Fatalf("edit form rendered Go value dump marker %q", unexpected)
		}
	}
	start := strings.Index(content, `name="host" value="`)
	if start < 0 {
		t.Fatal("custom domain input not found")
	}
	valueStart := start + len(`name="host" value="`)
	valueEnd := strings.Index(content[valueStart:], `"`)
	if valueEnd < 0 || content[valueStart:valueStart+valueEnd] != domain {
		t.Fatalf("custom domain input contains unexpected value: %q", content[valueStart:])
	}
	if strings.Contains(content, "synthetic-auth-value") || !strings.Contains(content, "已配置（不会回显账号或密码，留空表示保留）") {
		t.Fatal("edit form must show configured auth status without credentials")
	}

	post, recorder := newOwnerSwitchController(http.MethodPost, "EditHost", url.Values{
		"id": {"1"}, "client_id": {strconv.Itoa(newClient.Id)}, "domain_mode": {"custom"},
		"host": {domain}, "scheme": {"https"}, "location": {"/"}, "target": {host.Target.TargetStr},
	})
	runClientPermissionAction(t, post.EditHost)
	if reply := readClientPermissionResponse(t, recorder); reply["status"] != float64(1) {
		t.Fatalf("edit response = %#v", reply)
	}
	if got := savedRoute(t, storage, store.KindHost, host.Id).Client.Id; got != newClient.Id {
		t.Fatalf("saved client id = %d, want %d", got, newClient.Id)
	}
	get, _ = newOwnerSwitchController(http.MethodGet, "EditHost", url.Values{"id": {"1"}})
	get.EditHost()
	if view := get.Data["h"].(*hostFormView); view.Host != domain || view.Client.Id != newClient.Id {
		t.Fatalf("reopened edit form has domain %q and client %d", view.Host, view.Client.Id)
	}
	if host.Auth != auth || host.CertFilePath != "conf/certs/existing.pem" || host.KeyFilePath != "conf/certs/existing.key" {
		t.Fatal("changing client altered the saved host auth or certificate paths")
	}
}

func TestTunnelEditClientPreservesStoppedStateAndReportsBindFailure(t *testing.T) {
	previousPorts := beego.AppConfig.String("allow_ports")
	if err := beego.AppConfig.Set("allow_ports", ""); err != nil {
		t.Fatal(err)
	}
	tool.InitAllowPort()
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("allow_ports", previousPorts)
		tool.InitAllowPort()
	})
	for _, enabled := range []bool{false, true} {
		name := "stopped"
		if enabled {
			name = "bind failure reported before reply"
		}
		t.Run(name, func(t *testing.T) {
			db, storage, oldClient, newClient := ownerSwitchTestDB(t)
			occupied, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer occupied.Close()
			port := occupied.Addr().(*net.TCPAddr).Port
			task := &file.Tunnel{
				Id: 13, Mode: "tcp", Port: port, ServerIp: "127.0.0.1", Status: enabled,
				Client: oldClient, Target: &file.Target{TargetStr: "127.0.0.1:12345"}, Flow: file.NewFlow(),
			}
			db.JsonDb.Tasks.Store(task.Id, task)
			post, recorder := newOwnerSwitchController(http.MethodPost, "Edit", url.Values{
				"id": {"13"}, "client_id": {strconv.Itoa(newClient.Id)}, "type": {"tcp"},
				"port": {strconv.Itoa(port)}, "server_ip": {"127.0.0.1"}, "target": {task.Target.TargetStr},
			})
			runClientPermissionAction(t, post.Edit)
			reply := readClientPermissionResponse(t, recorder)
			if enabled {
				if reply["status"] != float64(0) || !strings.Contains(reply["msg"].(string), "已保存，但监听重载失败") {
					t.Fatalf("ambiguous bind failure response: %#v", reply)
				}
			} else if reply["status"] != float64(1) {
				t.Fatalf("stopped edit was not accepted: %#v", reply)
			}
			saved := savedRoute(t, storage, store.KindTunnel, task.Id)
			if saved.Client.Id != newClient.Id || saved.Status || (saved.RunError != "") != enabled {
				t.Fatalf("unexpected stored edit outcome: %#v", saved)
			}
			if _, running := server.RunList.Load(task.Id); running {
				t.Fatal("a listener was registered for the stopped or failed task")
			}
		})
	}
}
