package workerexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// excludeFromGit keeps files AO writes into the session's checkout (its
// Claude and Cursor hook settings) out of `git status` and out of anything
// the agent stages with `git add -A`. The patterns go in the checkout's own
// info/exclude, which git never commits or pushes, so the user's .gitignore
// is left alone. That matters when the session works in the user's or the
// template's checkout instead of a clone AO made for itself.
//
// It is best effort: a workspace that is not a git checkout, or an exclude
// file that cannot be written, leaves the files visible but never fails the
// launch. paths are relative to the workspace and use forward slashes.
func excludeFromGit(workspace string, paths ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// --git-path resolves info/exclude for linked worktrees and separate git
	// dirs too; it prints a path relative to the workspace or an absolute one.
	output, err := exec.CommandContext(ctx, "git", "-C", workspace, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return
	}
	excludePath := strings.TrimSpace(string(output))
	if excludePath == "" {
		return
	}
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(workspace, excludePath)
	}
	_ = appendExcludePatterns(excludePath, paths)
}

func appendExcludePatterns(excludePath string, paths []string) error {
	existing, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	present := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var missing bytes.Buffer
	for _, path := range paths {
		pattern := "/" + strings.TrimPrefix(path, "/")
		if !present[pattern] {
			present[pattern] = true
			missing.WriteString(pattern + "\n")
		}
	}
	if missing.Len() == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(excludePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	var block bytes.Buffer
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		block.WriteString("\n")
	}
	block.WriteString("# Written by AO for its agent hook settings\n")
	block.Write(missing.Bytes())
	if _, err := file.Write(block.Bytes()); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
