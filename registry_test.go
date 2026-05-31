package acpruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEnsureBinaryExtractsTarGzipArchive(t *testing.T) {
	t.Setenv(RuntimeCacheDirEnvVar, t.TempDir())
	server := serveTestArchive(t, []testTarEntry{
		{name: "bin/acp-agent", mode: 0o755, body: "agent"},
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path, err := ensureBinary(ctx, "test-agent", BinaryTarget{
		Archive: server.URL + "/agent.tar.gz",
		Cmd:     "./bin/acp-agent",
	})
	if err != nil {
		t.Fatalf("ensureBinary() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if string(data) != "agent" {
		t.Fatalf("extracted binary = %q, want agent", string(data))
	}
}

func TestEnsureBinaryRejectsArchivePathTraversal(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv(RuntimeCacheDirEnvVar, cacheDir)
	server := serveTestArchive(t, []testTarEntry{
		{name: "../escape", mode: 0o755, body: "bad"},
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ensureBinary(ctx, "test-agent", BinaryTarget{
		Archive: server.URL + "/agent.tar.gz",
		Cmd:     "./bin/acp-agent",
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe archive file") {
		t.Fatalf("ensureBinary() error = %v, want unsafe archive file", err)
	}
	if _, statErr := os.Stat(cacheDir + "/escape"); !os.IsNotExist(statErr) {
		t.Fatalf("path traversal wrote escape file, stat error = %v", statErr)
	}
}

func TestEnsureBinaryRejectsArchiveSymlink(t *testing.T) {
	t.Setenv(RuntimeCacheDirEnvVar, t.TempDir())
	server := serveTestArchive(t, []testTarEntry{
		{name: "bin/acp-agent", link: "/bin/sh"},
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ensureBinary(ctx, "test-agent", BinaryTarget{
		Archive: server.URL + "/agent.tar.gz",
		Cmd:     "./bin/acp-agent",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported archive entry") {
		t.Fatalf("ensureBinary() error = %v, want unsupported archive entry", err)
	}
}

type testTarEntry struct {
	name string
	mode int64
	body string
	link string
}

func serveTestArchive(t *testing.T, entries []testTarEntry) *httptest.Server {
	t.Helper()
	archive := makeTestTarGzip(t, entries)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
}

func makeTestTarGzip(t *testing.T, entries []testTarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		if entry.link != "" {
			if err := tw.WriteHeader(&tar.Header{Name: entry.name, Typeflag: tar.TypeSymlink, Linkname: entry.link}); err != nil {
				t.Fatalf("WriteHeader(symlink) error = %v", err)
			}
			continue
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: mode, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("WriteHeader(file) error = %v", err)
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatalf("Write(file) error = %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	return buf.Bytes()
}
