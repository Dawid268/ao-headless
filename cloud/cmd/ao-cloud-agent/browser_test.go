package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserSendsNamedFilesAndWritesReturnedOnes(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("notes.txt", []byte("relative"), 0o600); err != nil {
		t.Fatal(err)
	}
	absolute := filepath.Join(dir, "page.html")
	if err := os.WriteFile(absolute, []byte("absolute"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []map[string][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Args  []string          `json:"args"`
			Files map[string][]byte `json:"files"`
		}
		if r.URL.Path != "/agent" || r.Header.Get("Authorization") != "Bearer rb-token" ||
			json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		got = append(got, request.Files)
		response := map[string]any{"output": "ok\n", "files": map[string][]byte{"shots/a.png": []byte("png")}}
		if request.Args[0] == "upload" {
			response["error"] = "upload failed"
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	t.Setenv("RB_URL", "")
	if err := runBrowser([]string{"snapshot"}); err == nil || err.Error() != "this sandbox has no browser" {
		t.Fatalf("without RB_URL: %v, want the no-browser message", err)
	}
	t.Setenv("RB_URL", server.URL)
	t.Setenv("RB_TOKEN", "rb-token")

	// A relative name is page text, not a file, outside an upload.
	if err := runBrowser([]string{"fill", "@e1", "notes.txt", absolute}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got[0]["notes.txt"]; ok || string(got[0][absolute]) != "absolute" {
		t.Errorf("fill sent %v, want only the absolute file", got[0])
	}
	if data, err := os.ReadFile(filepath.Join(dir, "shots", "a.png")); err != nil || string(data) != "png" {
		t.Errorf("returned file = %q, %v", data, err)
	}

	err := runBrowser([]string{"upload", "@e2", "notes.txt"})
	if err == nil || err.Error() != "upload failed" {
		t.Fatalf("upload error = %v, want the browser's error", err)
	}
	if string(got[1]["notes.txt"]) != "relative" {
		t.Errorf("upload sent %v, want the relative file", got[1])
	}
}
