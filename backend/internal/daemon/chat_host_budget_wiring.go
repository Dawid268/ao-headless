package daemon

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/chathostbudget"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/procmem"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

type sessionKindReader interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
}

type chatHostWakeAdmittable interface {
	SetChatHostWakeAdmit(func(context.Context, domain.SessionID) error)
}

type pressureHibernator interface {
	HibernateChatForPressure(context.Context, domain.SessionID) (bool, error)
}

type wakeHibernator interface {
	WakeHibernatedChat(context.Context, domain.SessionID) error
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
	projectOf := func(hostID string) string {
		if sessions == nil {
			return ""
		}
		rec, found, err := sessions.GetSession(context.Background(), domain.SessionID(hostID))
		if err != nil || !found {
			return ""
		}
		return string(rec.ProjectID)
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
				budget.Enqueue(chathostbudget.QueuedWake{
					HostID: sessionID, ProjectID: projectOf(sessionID), Class: class,
				})
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

// wireChatHostWakeAdmit installs the soft-queue gate on Session Manager so
// ResumeAgentWithMode enqueues before beginAgentResume when capacity is full.
func wireChatHostWakeAdmit(budget *chathostbudget.Budget, mgr chatHostWakeAdmittable, sessions sessionKindReader, dataDir string) {
	if budget == nil || mgr == nil {
		return
	}
	mgr.SetChatHostWakeAdmit(func(ctx context.Context, id domain.SessionID) error {
		_ = ctx
		hostID := string(id)
		if _, alive := persistenthost.HostPID(dataDir, hostID); alive {
			return nil
		}
		class := chathostbudget.ClassWorker
		projectID := ""
		if sessions != nil {
			rec, found, err := sessions.GetSession(context.Background(), id)
			if err == nil && found {
				class = chathostbudget.Classify(hostID, rec.Kind == domain.KindOrchestrator)
				projectID = string(rec.ProjectID)
			}
		} else {
			class = chathostbudget.ClassifyHostID(hostID)
		}
		err := budget.SoftAdmitWake(hostID, class, projectID)
		if errors.Is(err, chathostbudget.ErrQueued) {
			return sessionmanager.ErrChatHostQueued
		}
		return err
	})
}

// reclaimChatHostsUnderPressure hibernates reclaimable warm workers when memory
// pressure (or over-cap holdings) requires free slots. Orchestrators are never
// auto-hibernated.
func reclaimChatHostsUnderPressure(
	ctx context.Context,
	budget *chathostbudget.Budget,
	hibernator pressureHibernator,
	log *slog.Logger,
) {
	if budget == nil || hibernator == nil {
		return
	}
	if !budget.PressureActive() && !budget.OverPressureCap() {
		return
	}
	for _, hostID := range budget.WorkerHostIDs() {
		if ctx.Err() != nil {
			return
		}
		stopped, err := hibernator.HibernateChatForPressure(ctx, domain.SessionID(hostID))
		if err != nil && log != nil && ctx.Err() == nil {
			log.Warn("pressure chat-host reclaim failed", "hostID", hostID, "err", err)
			continue
		}
		if stopped && log != nil {
			log.Info("pressure reclaim hibernated chat host", "hostID", hostID)
		}
	}
}

// drainChatHostWakeQueue admits queued wakes and starts them. Drain alone would
// leak slots; every admission must attempt a real wake.
func drainChatHostWakeQueue(
	ctx context.Context,
	budget *chathostbudget.Budget,
	wake wakeHibernator,
	log *slog.Logger,
) {
	if budget == nil || wake == nil {
		return
	}
	admitted := budget.Drain(ctx)
	for _, item := range admitted {
		go func(w chathostbudget.QueuedWake) {
			wakeCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := wake.WakeHibernatedChat(wakeCtx, domain.SessionID(w.HostID)); err != nil {
				budget.ForceRelease(w.HostID)
				budget.Enqueue(w)
				if log != nil {
					log.Info("queued chat host wake failed; requeued", "hostID", w.HostID, "err", err.Error())
				}
			}
		}(item)
	}
}
