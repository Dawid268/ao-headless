package updatedrain

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type gate struct{ closed bool }

func (g *gate) SetAdmitsClosed(closed bool) { g.closed = closed }
func (g *gate) AdmitsClosed() bool          { return g.closed }

func TestBeginThenClear(t *testing.T) {
	dir := t.TempDir()
	g := &gate{}
	svc := &Service{DataDir: dir, Gate: g}
	st, err := svc.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Draining || !st.SafeToReplace || !g.closed {
		t.Fatalf("begin = %+v closed=%v", st, g.closed)
	}
	if _, err := os.Stat(filepath.Join(dir, flagName)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Clear(); err != nil {
		t.Fatal(err)
	}
	if g.closed {
		t.Fatal("clear must reopen admits")
	}
	st, err = svc.Status(context.Background())
	if err != nil || st.Draining {
		t.Fatalf("status after clear = %+v err=%v", st, err)
	}
}
