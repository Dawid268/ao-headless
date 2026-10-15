package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// runBrowser runs an agent-browser command in the sandbox's browser, the one
// the person watches in AO's Browser panel. Files travel with the command:
// local files it names go up, files it writes (screenshot, pdf, download)
// come back. RB_URL is the browser service; RB_TOKEN is its token.
func runBrowser(args []string) error {
	base, token := os.Getenv("RB_URL"), os.Getenv("RB_TOKEN")
	if base == "" || token == "" {
		return errors.New("this sandbox has no browser")
	}
	if len(args) == 0 {
		return errors.New("usage: ao browser <agent-browser command> [args...]")
	}
	// Relative names go up for an upload only: a word typed into a page
	// (README.md, .env) is no file to copy.
	upload := false
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			upload = arg == "upload"
			break
		}
	}
	files := map[string][]byte{}
	for _, arg := range args {
		if !upload && !filepath.IsAbs(arg) {
			continue
		}
		// An argument no file can have is just text.
		if info, err := os.Stat(arg); err == nil && info.Mode().IsRegular() {
			data, err := os.ReadFile(arg)
			if err != nil {
				return err
			}
			files[arg] = data
		}
	}
	body, err := json.Marshal(map[string]any{"args": args, "files": files})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/agent", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	// No timeout: a command may wait on the page as long as it was told to.
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("browser: %w", err)
	}
	defer response.Body.Close()
	var result struct {
		Output string            `json:"output"`
		Error  string            `json:"error"`
		Files  map[string][]byte `json:"files"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return fmt.Errorf("browser: %s", response.Status)
	}
	for path, data := range result.Files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	fmt.Print(result.Output)
	if result.Error != "" {
		return errors.New(result.Error)
	}
	return nil
}
