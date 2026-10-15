package project

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Seed limits keep a laptop→VPS import from OOM'ing the host or ingesting junk.
const (
	seedMaxBytes     = 512 << 20 // 512 MiB uncompressed budget
	seedMaxFiles     = 50000
	seedMaxFileBytes = 64 << 20 // 64 MiB per file
	// SeedMaxUploadBytes caps the compressed HTTP upload body (gzip tar).
	SeedMaxUploadBytes = 600 << 20
)

// SeedMaxArchiveBytes is the HTTP body limit for POST /projects/seed.
func SeedMaxArchiveBytes() int64 { return SeedMaxUploadBytes }

var seedDenyDirNames = map[string]struct{}{
	"node_modules": {}, ".next": {}, "dist": {}, "build": {}, "coverage": {},
	".turbo": {}, ".cache": {}, "__pycache__": {}, ".venv": {}, "venv": {},
	"target": {}, ".git": {},
}

var seedDenyFilePrefixes = []string{".env", "credentials", "id_rsa", "id_ed25519"}
var seedDenyFileSuffixes = []string{".pem", ".p12", ".key"}

// SeedArchiveInput is a gzipped tar stream of a local folder (source only).
type SeedArchiveInput struct {
	// DestDir is the absolute host path that will become the project root.
	DestDir string
	// IncludeSecrets allows .env* / key material (default false).
	IncludeSecrets bool
}

// SeedArchiveResult reports where the tree was written.
type SeedArchiveResult struct {
	Path string `json:"path"`
}

// SeedArchive unpacks a gzipped tar into DestDir with deny-by-default filters,
// then writes a generated .gitignore so InitializeRepository does not commit
// node_modules. Caller should Invoke InitializeRepository + Add afterward.
func SeedArchive(ctx context.Context, r io.Reader, in SeedArchiveInput) (SeedArchiveResult, error) {
	if err := ctx.Err(); err != nil {
		return SeedArchiveResult{}, err
	}
	dest := filepath.Clean(strings.TrimSpace(in.DestDir))
	if !filepath.IsAbs(dest) {
		return SeedArchiveResult{}, apierr.Invalid("SEED_DEST_NOT_ABSOLUTE", "Seed destination must be an absolute path.", nil)
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return SeedArchiveResult{}, fmt.Errorf("create seed destination: %w", err)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return SeedArchiveResult{}, apierr.Invalid("SEED_NOT_GZIP", "Seed archive must be gzip-compressed tar.", map[string]any{"error": err.Error()})
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var totalBytes int64
	var files int
	for {
		if err := ctx.Err(); err != nil {
			return SeedArchiveResult{}, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return SeedArchiveResult{}, apierr.Invalid("SEED_TAR_INVALID", "Could not read seed archive entry.", map[string]any{"error": err.Error()})
		}
		name := filepath.ToSlash(hdr.Name)
		name = strings.TrimPrefix(name, "./")
		if name == "" || name == "." {
			continue
		}
		if strings.Contains(name, "..") {
			return SeedArchiveResult{}, apierr.Invalid("SEED_PATH_TRAVERSAL", "Seed archive entry escapes the destination.", map[string]any{"path": hdr.Name})
		}
		if shouldDenySeedPath(name, in.IncludeSecrets) {
			if hdr.Typeflag == tar.TypeDir {
				continue
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(tr, hdr.Size+1))
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if !strings.HasPrefix(target, dest+string(os.PathSeparator)) && target != dest {
			return SeedArchiveResult{}, apierr.Invalid("SEED_PATH_TRAVERSAL", "Seed archive entry escapes the destination.", map[string]any{"path": hdr.Name})
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return SeedArchiveResult{}, err
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			if files > seedMaxFiles {
				return SeedArchiveResult{}, apierr.Invalid("SEED_TOO_MANY_FILES", "Seed archive has too many files.", map[string]any{"max": seedMaxFiles})
			}
			if hdr.Size > seedMaxFileBytes {
				return SeedArchiveResult{}, apierr.Invalid("SEED_FILE_TOO_LARGE", "Seed file exceeds the per-file limit.", map[string]any{"path": name, "maxBytes": seedMaxFileBytes})
			}
			totalBytes += hdr.Size
			if totalBytes > seedMaxBytes {
				return SeedArchiveResult{}, apierr.Invalid("SEED_TOO_LARGE", "Seed archive exceeds the size limit.", map[string]any{"maxBytes": seedMaxBytes})
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return SeedArchiveResult{}, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return SeedArchiveResult{}, err
			}
			written, copyErr := io.Copy(f, io.LimitReader(tr, hdr.Size+1))
			closeErr := f.Close()
			if copyErr != nil {
				return SeedArchiveResult{}, copyErr
			}
			if written > hdr.Size {
				return SeedArchiveResult{}, apierr.Invalid("SEED_FILE_TOO_LARGE", "Seed file exceeded declared size.", map[string]any{"path": name})
			}
			if closeErr != nil {
				return SeedArchiveResult{}, closeErr
			}
		default:
			// Skip symlinks and specials in MVP (symlink escape risk).
			continue
		}
	}
	if err := writeSeedGitignore(dest); err != nil {
		return SeedArchiveResult{}, err
	}
	return SeedArchiveResult{Path: dest}, nil
}

func shouldDenySeedPath(slashPath string, includeSecrets bool) bool {
	parts := strings.Split(slashPath, "/")
	for _, p := range parts {
		if _, ok := seedDenyDirNames[p]; ok {
			return true
		}
	}
	base := parts[len(parts)-1]
	if !includeSecrets {
		lower := strings.ToLower(base)
		for _, prefix := range seedDenyFilePrefixes {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
		for _, suffix := range seedDenyFileSuffixes {
			if strings.HasSuffix(lower, suffix) {
				return true
			}
		}
	}
	return false
}

func writeSeedGitignore(dest string) error {
	const body = `# Generated by AO project seed — keep worktrees thin.
node_modules/
.npm/
.next/
dist/
build/
coverage/
.turbo/
.cache/
__pycache__/
.venv/
venv/
target/
.env
.env.*
*.pem
*.p12
`
	path := filepath.Join(dest, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil // preserve caller ignore
	}
	return os.WriteFile(path, []byte(body), 0o600)
}
