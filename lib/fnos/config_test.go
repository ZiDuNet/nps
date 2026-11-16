package fnos

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ehang.io/nps/lib/config"
	"ehang.io/nps/lib/quickcommand"
)

func env(values map[string]string) LookupEnv {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

func fields(old Config, mode string) map[string]string {
	p := FieldPrefix("new", old.Revision)
	return map[string]string{
		p + "action": "add", p + "mode": mode, p + "enabled": "true",
		p + "host": "example.com", p + "port": "8024", p + "vkey": "test-secret", p + "protocol": "tcp",
	}
}

func entry(t *testing.T, id, key string) Entry {
	t.Helper()
	value, err := ParseEntry(id, "[common]\nserver_addr=example.com:8024\nvkey="+key+"\n")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustQuick(t *testing.T, command quickcommand.Command) string {
	t.Helper()
	encoded, err := quickcommand.Encode(command)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestApplyManualTransports(t *testing.T) {
	for _, protocol := range []string{"tcp", "tls", "kcp", "ws", "wss", "quic"} {
		t.Run(protocol, func(t *testing.T) {
			old := NewConfig()
			values := fields(old, "manual")
			p := FieldPrefix("new", old.Revision)
			values[p+"protocol"] = protocol
			values[p+"host"] = "2001:db8::1"
			values[p+"path"] = "/bridge"
			values[p+"alpn"] = "nps-test"
			updated, err := Apply(old, env(values))
			if err != nil {
				t.Fatal(err)
			}
			e := updated.Connections[0]
			if e.ID != MainID || e.Command.Server != "[2001:db8::1]:8024" || !e.Enabled || e.Sync {
				t.Fatal("unexpected connection metadata")
			}
			if e.Command.Transport == "tcp" && e.Command.TLS != (protocol == "tls") {
				t.Fatal("TLS alias was not preserved")
			}
			if (protocol == "ws" || protocol == "wss") && e.Command.BridgePath != "/bridge" {
				t.Fatal("WebSocket path was lost")
			}
			if protocol == "quic" && e.Command.QuicALPN != "nps-test" {
				t.Fatal("ALPN was lost")
			}
		})
	}
}

func TestQuickV1V2AndBlankPreservesSecret(t *testing.T) {
	for _, encoded := range []string{
		quickcommand.EncodeLegacy("legacy", "example.com:8025", "never-log-key", true),
		mustQuick(t, quickcommand.Command{Server: "example.com:8026", VKey: "never-log-key", Transport: "wss", BridgePath: "/bridge"}),
	} {
		old := NewConfig()
		values := fields(old, "quick")
		values[FieldPrefix("new", old.Revision)+"quick"] = encoded
		old, err := Apply(old, env(values))
		if err != nil {
			t.Fatal(err)
		}
		e := old.Connections[0]
		p := FieldPrefix(e.ID, old.Revision)
		updated, err := Apply(old, env(map[string]string{
			p + "action": "update", p + "mode": "quick", p + "enabled": "false", p + "name": "changed",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if updated.Connections[0].Enabled || updated.Connections[0].Command.VKey != "never-log-key" || updated.Connections[0].Command.Name != "changed" {
			t.Fatal("empty quick input did not retain credentials")
		}
		wizard, err := Wizard(updated, false)
		if err != nil || !json.Valid(wizard) || strings.Contains(string(wizard), "never-log-key") || strings.Contains(string(wizard), encoded) {
			t.Fatal("native wizard is invalid or contains a secret")
		}
	}
}

func TestNativeTemplateAndManualRulesArePreserved(t *testing.T) {
	template, err := os.ReadFile("../../conf/npc.conf")
	if err != nil {
		t.Fatal(err)
	}
	old := NewConfig()
	updated, err := Apply(old, env(fields(old, "manual")), string(template))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	wizard := filepath.Join(t.TempDir(), "config")
	if err := SaveWithWizard(root, wizard, updated); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "npc.conf")
	native, err := config.NewConfig(path)
	if err != nil || native.CommonConfig.VKey != "test-secret" || len(native.Tasks) != 0 || len(native.Hosts) != 0 {
		t.Fatal("first-run native template was not usable, or examples became active")
	}
	raw := updated.Connections[0].Raw
	if !strings.Contains(raw, "#dns_server=") || !strings.Contains(raw, "#[tcp_ssh]") || !strings.Contains(raw, "#[health_api]") {
		t.Fatal("full template fields/examples were lost")
	}
	rules := "\n# handwritten rule\n[ssh]\nmode=tcp\nserver_port=10022\ntarget_addr=127.0.0.1:22\n"
	raw = strings.Replace(raw, "#dns_server=1.1.1.1,8.8.8.8", "dns_server=1.1.1.1,8.8.8.8", 1) + rules
	if err := AtomicWrite(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	old, err = Read(root)
	if err != nil || !old.Connections[0].Sync || old.Revision == updated.Revision {
		t.Fatal("manual edits were not detected")
	}
	values := fields(old, "manual")
	values[FieldPrefix("new", old.Revision)+"action"] = "skip"
	p := FieldPrefix(MainID, old.Revision)
	for key, value := range map[string]string{"action": "update", "mode": "manual", "enabled": "false", "host": "other.example.com", "port": "8025", "protocol": "tls"} {
		values[p+key] = value
	}
	updated, err = Apply(old, env(values))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(updated.Connections[0].Raw, rules) || !strings.Contains(updated.Connections[0].Raw, "dns_server=1.1.1.1,8.8.8.8") || updated.Connections[0].Command.VKey != "test-secret" {
		t.Fatal("wizard destroyed unmanaged fields, rules, or an existing secret")
	}
	if err := SaveWithWizard(root, wizard, updated); err != nil {
		t.Fatal(err)
	}
	read, err := Read(root)
	if err != nil || read.Revision != updated.Revision {
		t.Fatal("saved configuration revision was inconsistent")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("npc.conf must be private")
	}
}

func TestConfigRejectsStaleDuplicateInvalidAndSymlink(t *testing.T) {
	// A stale remembered form no longer matches revision-scoped field names and
	// must stay a safe no-op instead of replaying an add or delete.
	stale := NewConfig()
	current := NewConfig()
	current.Connections = []Entry{entry(t, MainID, "stale-key")}
	current.Revision = Revision(current)
	if updated, err := Apply(current, env(fields(stale, "manual"))); err != nil || len(updated.Connections) != 1 {
		t.Fatal("stale form was not a safe no-op")
	}
	e := entry(t, MainID, "redact-me")
	dup := current
	dup.Connections = []Entry{e, e}
	if err := dup.Validate(); err == nil {
		t.Fatal("accepted duplicate IDs")
	}
	for _, text := range []string{
		"[common]\nserver_addr=example.com:0\nvkey=redact-me\n",
		"[common]\nserver_addr=example.com:8024\nvkey=redact-me\ntls_insecure_skip_verify=true\n",
		"[common]\n[common]\nvkey=redact-me\n",
		"[common]\nvkey=redact-me\nserver_addr=evil host:8000\n",
	} {
		if _, err := ParseEntry(MainID, text); err == nil || strings.Contains(err.Error(), "redact-me") {
			t.Fatal("invalid input accepted or credentials echoed")
		}
	}
	root := t.TempDir()
	other := filepath.Join(t.TempDir(), "safe")
	if err := os.WriteFile(other, []byte(e.Raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "npc.conf")); err != nil {
		t.Skip(err)
	}
	if _, err := Read(root); err == nil {
		t.Fatal("read a linked configuration")
	}
	if err := AtomicWrite(filepath.Join(root, "npc.conf"), []byte("bad"), 0600); err == nil {
		t.Fatal("overwrote a linked configuration")
	}
}

func TestDeleteRequiresExplicitConfirmation(t *testing.T) {
	old := NewConfig()
	old.Connections = []Entry{entry(t, MainID, "test-key")}
	old.Revision = Revision(old)
	p := FieldPrefix(MainID, old.Revision)
	values := map[string]string{p + "action": "delete"}
	if _, err := Apply(old, env(values)); err == nil {
		t.Fatal("deleted without confirmation")
	}
	values[p+"confirm_delete"] = "yes"
	updated, err := Apply(old, env(values))
	if err != nil || len(updated.Connections) != 0 {
		t.Fatal("confirmed delete failed")
	}
}
