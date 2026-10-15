package persistenthost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestSpawnAdmissionBlocksBeforeDetachedLaunch(t *testing.T) {
	t.Cleanup(func() { SetSpawnAdmission(nil) })
	var before atomic.Int32
	SetSpawnAdmission(&SpawnAdmission{
		BeforeSpawn: func(string) error {
			before.Add(1)
			return errors.New("no capacity")
		},
	})
	dir := t.TempDir()
	_, err := ConnectOrStart(context.Background(), Config{
		SessionID: "blocked",
		DataDir:   dir,
		Workdir:   dir,
		Argv:      []string{"/bin/true"},
	})
	if err == nil || err.Error() != "no capacity" {
		t.Fatalf("err = %v", err)
	}
	if before.Load() != 1 {
		t.Fatalf("BeforeSpawn calls = %d", before.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "chat-hosts", "blocked", "host.json")); !os.IsNotExist(err) {
		t.Fatalf("host should not have been spawned: %v", err)
	}
}
