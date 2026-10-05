package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.4", "v0.1.5", true},
		{"v0.1.9", "v0.2.0", true},
		{"v0.9.0", "v0.10.0", true},
		{"v1.0.0", "v0.9.9", false},
		{"v0.1.5", "v0.1.5", false},
		{"v0.1.5-rc.1", "v0.1.5", false},
		{"dev", "v0.1.5", false},
		{"v0.1.4-5-g525dadc", "v0.1.5", false},
		{"525dadc", "v0.1.5", false},
		{"v0.1.4", "garbage", false},
	} {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

// release serves a release of a fake binary, the way the download server lays it out.
func release(t *testing.T, binary []byte, sum string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "pantech", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(binary)
	_ = tw.Close()
	_ = gz.Close()
	archive := buf.Bytes()
	if sum == "" {
		h := sha256.Sum256(archive)
		sum = hex.EncodeToString(h[:])
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("v0.1.5\n")) })
	mux.HandleFunc("/v0.1.5/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sum + "  pantech_" + Platform() + ".tar.gz\n"))
	})
	mux.HandleFunc("/v0.1.5/pantech_"+Platform()+".tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = old })
}

func oldBinary(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "pantech")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestLatest(t *testing.T) {
	release(t, []byte("new"), "")
	if v, err := Latest(context.Background()); err != nil || v != "v0.1.5" {
		t.Fatalf("Latest = %q, %v", v, err)
	}
}

func TestInstallReplacesTheBinary(t *testing.T) {
	release(t, []byte("new"), "")
	exe := oldBinary(t)
	if err := Install(context.Background(), "v0.1.5", exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	info, _ := os.Stat(exe)
	if string(got) != "new" || info.Mode().Perm() != 0o755 {
		t.Fatalf("binary %q, mode %v", got, info.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".pantech-*")); len(left) != 0 {
		t.Fatalf("left behind %v", left)
	}
}

func TestInstallRefusesABadChecksum(t *testing.T) {
	release(t, []byte("new"), "0000000000000000000000000000000000000000000000000000000000000000")
	exe := oldBinary(t)
	if err := Install(context.Background(), "v0.1.5", exe); err == nil {
		t.Fatal("installed a download that does not match its checksum")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("binary is now %q", got)
	}
}

func TestInstallUnknownVersion(t *testing.T) {
	release(t, []byte("new"), "")
	if err := Install(context.Background(), "v9.9.9", oldBinary(t)); err == nil {
		t.Fatal("want an error for a version that does not exist")
	}
}

func TestState(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dir", "update-check.json")
	if s := LoadState(p); !s.CheckedAt.IsZero() {
		t.Fatalf("missing file: %+v", s)
	}
	now := time.Now().Truncate(time.Second)
	SaveState(p, State{CheckedAt: now, Latest: "v0.1.5"})
	if s := LoadState(p); !s.CheckedAt.Equal(now) || s.Latest != "v0.1.5" {
		t.Fatalf("read back %+v", s)
	}
}
