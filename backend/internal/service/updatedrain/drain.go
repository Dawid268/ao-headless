// Package updatedrain pauses new chat-host admits so a container image can be
// replaced without accepting new work. It does not stop projects.
package updatedrain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

const flagName = "update-drain"

// Status is the public update-drain view. It never includes secrets.
type Status struct {
	Draining      bool `json:"draining"`
	SafeToReplace bool `json:"safeToReplace"`
}

// Gate closes or reopens SoftAdmit-style wake admission.
type Gate interface {
	SetAdmitsClosed(closed bool)
	AdmitsClosed() bool
}

// Service stores the drain flag under the daemon data dir and toggles Gate.
type Service struct {
	DataDir string
	Gate    Gate
}

func flagPath(dataDir string) string {
	return filepath.Join(dataDir, flagName)
}

// Status reports whether admits are closed or the on-disk flag is present.
func (s *Service) Status(context.Context) (Status, error) {
	draining := s != nil && s.Gate != nil && s.Gate.AdmitsClosed()
	if s != nil && !draining {
		_, err := os.Stat(flagPath(s.DataDir))
		draining = err == nil
	}
	return Status{Draining: draining, SafeToReplace: draining}, nil
}

// Begin closes new admits and writes the flag so a restarting entrypoint can
// see that this process was asked to quiesce.
func (s *Service) Begin(context.Context) (Status, error) {
	if s == nil || s.DataDir == "" {
		return Status{}, errors.New("update drain: data dir is required")
	}
	if err := os.MkdirAll(s.DataDir, 0o755); err != nil {
		return Status{}, err
	}
	if err := os.WriteFile(flagPath(s.DataDir), []byte("1\n"), 0o600); err != nil {
		return Status{}, err
	}
	if s.Gate != nil {
		s.Gate.SetAdmitsClosed(true)
	}
	return Status{Draining: true, SafeToReplace: true}, nil
}

// Clear removes the flag and reopens admits. Call this on a fresh boot so a
// replacement process does not stay drained.
func (s *Service) Clear() error {
	if s == nil {
		return nil
	}
	if s.Gate != nil {
		s.Gate.SetAdmitsClosed(false)
	}
	err := os.Remove(flagPath(s.DataDir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
