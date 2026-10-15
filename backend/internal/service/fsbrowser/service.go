// Package fsbrowser provides directory listings and mkdir for remote clients.
package fsbrowser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

const maxEntries = 500

// Entry is one visible subdirectory.
type Entry struct {
	Name    string
	Path    string
	GitRepo bool
}

// Listing is a bounded directory listing on the daemon host.
type Listing struct {
	Path      string
	Parent    string
	Entries   []Entry
	Truncated bool
}

// Service browses the daemon host's filesystem without exposing file contents.
type Service struct{}

// New constructs a directory browser backed by the host filesystem.
func New() *Service { return &Service{} }

func resolveAbsDir(path string) (string, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Clean(home), nil
	}
	if !filepath.IsAbs(path) {
		return "", apierr.Invalid("FS_PATH_NOT_ABSOLUTE", "path must be absolute on the daemon host", nil)
	}
	return filepath.Clean(path), nil
}

// List returns the visible subdirectories of path. An empty path uses the
// daemon user's home directory.
func (*Service) List(ctx context.Context, path string) (Listing, error) {
	if err := ctx.Err(); err != nil {
		return Listing{}, err
	}
	path, err := resolveAbsDir(path)
	if err != nil {
		return Listing{}, err
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return Listing{}, apierr.NotFound("FS_NOT_FOUND", "no such directory on the daemon host")
		case errors.Is(err, os.ErrPermission):
			return Listing{}, apierr.Forbidden("FS_FORBIDDEN", "the daemon may not read that directory")
		default:
			return Listing{}, apierr.Invalid("FS_NOT_A_DIRECTORY", "not a directory", nil)
		}
	}

	out := Listing{Path: path, Parent: filepath.Dir(path), Entries: []Entry{}}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if len(out.Entries) == maxEntries {
			out.Truncated = true
			break
		}
		child := filepath.Join(path, entry.Name())
		// .git may be a directory (normal clone) or a file (worktree): both are repos.
		_, gitErr := os.Stat(filepath.Join(child, ".git"))
		out.Entries = append(out.Entries, Entry{Name: entry.Name(), Path: child, GitRepo: gitErr == nil})
	}
	return out, nil
}

// Mkdir creates path (and parents) on the daemon host. Existing directories are
// accepted so transfer pickers can create-or-use destinations idempotently.
func (s *Service) Mkdir(ctx context.Context, path string) (Listing, error) {
	if err := ctx.Err(); err != nil {
		return Listing{}, err
	}
	path, err := resolveAbsDir(path)
	if err != nil {
		return Listing{}, err
	}
	if path == string(filepath.Separator) {
		return Listing{}, apierr.Invalid("FS_PATH_INVALID", "cannot create the filesystem root", nil)
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.IsDir() {
			return Listing{}, apierr.Invalid("FS_EXISTS_NOT_DIR", "path exists and is not a directory", nil)
		}
		return s.List(ctx, path)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		if errors.Is(statErr, os.ErrPermission) {
			return Listing{}, apierr.Forbidden("FS_FORBIDDEN", "the daemon may not inspect that path")
		}
		return Listing{}, apierr.Invalid("FS_STAT_FAILED", "could not inspect path", nil)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		switch {
		case errors.Is(err, os.ErrPermission):
			return Listing{}, apierr.Forbidden("FS_FORBIDDEN", "the daemon may not create that directory")
		default:
			return Listing{}, apierr.Invalid("FS_MKDIR_FAILED", "could not create directory", map[string]any{"error": err.Error()})
		}
	}
	return s.List(ctx, path)
}
