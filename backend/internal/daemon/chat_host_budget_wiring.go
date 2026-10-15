package daemon

import (
	"context"
	"errors"
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/chathostbudget"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/procmem"
)

type sessionKindReader interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
}

// installChatHostBudget wires the warm-host admission gate into persistenthost
// ConnectOrStart. Call Seed after reconcilePersistentChatHosts on boot.
func installChatHostBudget(dataDir string, sessions sessionKindReader, log *slog.Logger) *chathostbudget.Budget {
	orch := func(hostID string) bool {
		if sessions == nil {
			return false
		}
		rec, found, err := sessions.GetSession(context.Background(), domain.SessionID(hostID))
		if err != nil || !found {
			return false
		}
		return rec.Kind == domain.KindOrchestrator
	}
	budget := chathostbudget.New(chathostbudget.CapsFromEnv(), func(hostID string) bool {
		_, ok := persistenthost.HostPID(dataDir, hostID)
		return ok
	}, func() (uint64, error) {
		sys, err := procmem.ReadSystem()
		if err != nil {
			return 0, err
		}
		return sys.AvailableBytes, nil
	})
	persistenthost.SetSpawnAdmission(&persistenthost.SpawnAdmission{
		BeforeSpawn: func(sessionID string) error {
			class := chathostbudget.Classify(sessionID, orch(sessionID))
			err := budget.TryAcquireSpawn(sessionID, class)
			if errors.Is(err, chathostbudget.ErrNoCapacity) {
				budget.Enqueue(chathostbudget.QueuedWake{HostID: sessionID, Class: class})
				if log != nil {
					log.Info("chat host spawn queued: warm capacity exhausted", "hostID", sessionID)
				}
			} else if err != nil && log != nil {
				log.Info("chat host spawn deferred by warm budget", "hostID", sessionID, "err", err.Error())
			}
			return err
		},
		NoteAdopt: func(sessionID string) {
			budget.NoteAdopt(sessionID, chathostbudget.Classify(sessionID, orch(sessionID)))
		},
		AfterFailedSpawn: func(sessionID string, hostStillAlive bool) {
			if hostStillAlive {
				return
			}
			budget.ForceRelease(sessionID)
		},
	})
	return budget
}
