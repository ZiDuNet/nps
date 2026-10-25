package file

import "testing"

func TestFileAccessKeyIsStablePrivateAndScoped(t *testing.T) {
	key := FileAccessKey("verify-secret", "/srv/share", "/files/")
	if len(key) != 32 {
		t.Fatalf("FileAccessKey length = %d, want 32", len(key))
	}
	if key != FileAccessKey("verify-secret", "/srv/share", "/files/") {
		t.Fatal("FileAccessKey is not stable")
	}
	if key == FileAccessKey("other-secret", "/srv/share", "/files/") {
		t.Fatal("different VKEYs produced the same file key")
	}
	if key == FileAccessKey("verify-secret", "/srv/other", "/files/") {
		t.Fatal("different local paths produced the same file key")
	}
	if key == "verify-secret" || key == "" {
		t.Fatal("file key must be non-empty and must not expose the VKEY")
	}
}

func TestParseFileAccessTarget(t *testing.T) {
	key := FileAccessKey("verify-secret", "/srv/share", "/files/")
	got, ok := ParseFileAccessTarget(" file://" + key + " ")
	if !ok || got != key {
		t.Fatalf("ParseFileAccessTarget() = (%q, %t), want (%q, true)", got, ok, key)
	}
	for _, invalid := range []string{"", "file://", "file://../../etc/passwd", "file://001122", "http://example.com"} {
		if _, ok := ParseFileAccessTarget(invalid); ok {
			t.Errorf("ParseFileAccessTarget(%q) unexpectedly succeeded", invalid)
		}
	}
}
