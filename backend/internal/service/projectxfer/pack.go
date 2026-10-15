package projectxfer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
)

func writePackage(
	w io.Writer,
	manifest Manifest,
	settings SettingsPayload,
	sessions []SessionIndexEntry,
	conversations []ConversationExport,
	historyMD string,
	sourceTar, attachmentsTar, artifactsTar []byte,
) error {
	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)
	addJSON := func(name string, v any) error {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		return writeTarFile(tw, name, b)
	}
	if err := addJSON(PartManifest, manifest); err != nil {
		return err
	}
	if err := addJSON(PartSettings, settings); err != nil {
		return err
	}
	if err := addJSON(PartSessions, sessions); err != nil {
		return err
	}
	if err := addJSON(PartConversations, conversations); err != nil {
		return err
	}
	if err := writeTarFile(tw, PartHistoryMD, []byte(historyMD)); err != nil {
		return err
	}
	if err := writeTarFile(tw, PartSourceTar, sourceTar); err != nil {
		return err
	}
	if len(attachmentsTar) > 0 {
		if err := writeTarFile(tw, PartAttachments, attachmentsTar); err != nil {
			return err
		}
	}
	if len(artifactsTar) > 0 {
		if err := writeTarFile(tw, PartArtifacts, artifactsTar); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gw.Close()
}

func writeTarFile(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func readPackage(r io.Reader) (map[string][]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("not a gzip package: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.ToSlash(hdr.Name)
		name = strings.TrimPrefix(name, "./")
		body, err := io.ReadAll(io.LimitReader(tr, projectsvc.SeedMaxArchiveBytes()+1))
		if err != nil {
			return nil, err
		}
		out[name] = body
	}
	if _, ok := out[PartManifest]; !ok {
		return nil, fmt.Errorf("package missing manifest.json")
	}
	return out, nil
}

// packSourceTree reuses SeedArchive deny filters by piping through a temp pack.
func packSourceTree(root string, w io.Writer, includeSecrets bool) error {
	root = filepath.Clean(root)
	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if shouldDeny(slash, d.IsDir(), includeSecrets) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = slash
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		_ = f.Close()
		return copyErr
	})
	if err != nil {
		_ = tw.Close()
		_ = gw.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		_ = gw.Close()
		return err
	}
	return gw.Close()
}

func shouldDeny(slash string, isDir, includeSecrets bool) bool {
	// Match project.SeedArchive deny sets.
	base := slash
	if i := strings.LastIndex(slash, "/"); i >= 0 {
		base = slash[i+1:]
	}
	denyDirs := map[string]struct{}{
		"node_modules": {}, ".next": {}, "dist": {}, "build": {}, "coverage": {},
		".turbo": {}, ".cache": {}, "__pycache__": {}, ".venv": {}, "venv": {},
		"target": {}, ".git": {},
	}
	parts := strings.Split(slash, "/")
	for _, p := range parts {
		if _, ok := denyDirs[p]; ok {
			return true
		}
	}
	if isDir {
		return false
	}
	if includeSecrets {
		return false
	}
	lower := strings.ToLower(base)
	for _, p := range []string{".env", "credentials", "id_rsa", "id_ed25519"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	for _, s := range []string{".pem", ".p12", ".key"} {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

func packDataSubdir(dataDir, kind string, sessionIDs []string, w io.Writer) (bool, error) {
	if dataDir == "" || len(sessionIDs) == 0 {
		return false, nil
	}
	root := filepath.Join(dataDir, kind)
	if _, err := os.Stat(root); err != nil {
		return false, nil
	}
	buf := &bytes.Buffer{}
	gw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gw)
	wrote := false
	for _, id := range sessionIDs {
		dir := filepath.Join(root, id)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() {
				return err
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(rel)
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, f)
			_ = f.Close()
			if copyErr == nil {
				wrote = true
			}
			return copyErr
		})
	}
	_ = tw.Close()
	_ = gw.Close()
	if !wrote {
		return false, nil
	}
	_, err := w.Write(buf.Bytes())
	return true, err
}

func unpackDataSubdir(dataDir, kind string, r io.Reader) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	root := filepath.Join(dataDir, kind)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.ToSlash(hdr.Name)
		if strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, tr)
		_ = f.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}
