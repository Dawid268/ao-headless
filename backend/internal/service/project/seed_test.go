package project

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSeedArchiveFiltersAndWritesGitignore(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, body string) {
		t.Helper()
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	add("app/main.go", "package main\n")
	add("app/node_modules/leftpad/index.js", "module.exports=1\n")
	add("app/.env", "SECRET=1\n")
	add("app/README.md", "hi\n")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	out, err := SeedArchive(context.Background(), bytes.NewReader(buf.Bytes()), SeedArchiveInput{DestDir: dest})
	if err != nil {
		t.Fatal(err)
	}
	if out.Path != dest {
		t.Fatalf("path = %q", out.Path)
	}
	if _, err := os.Stat(filepath.Join(dest, "app", "main.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "app", "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "app", "node_modules", "leftpad", "index.js")); !os.IsNotExist(err) {
		t.Fatalf("node_modules should be denied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "app", ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env should be denied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".gitignore")); err != nil {
		t.Fatal(err)
	}
}

func TestSeedArchiveRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "../evil", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	_, err := SeedArchive(context.Background(), bytes.NewReader(buf.Bytes()), SeedArchiveInput{DestDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected traversal error")
	}
}
