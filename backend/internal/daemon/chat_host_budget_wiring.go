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
	SetChatHostWakeAdmit(func(context.Context, domain.SessionID) (reserved bool, err error))
	SetChatHostWakeRelease(func(domain.SessionID))
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
	mgr.SetChatHostWakeAdmit(func(ctx context.Context, id domain.SessionID) (bool, error) {
		_ = ctx
		hostID := string(id)
		if _, alive := persistenthost.HostPID(dataDir, hostID); alive {
			return false, nil
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
		reserved, err := budget.SoftAdmitWake(hostID, class, projectID)
		if errors.Is(err, chathostbudget.ErrQueued) {
			return false, sessionmanager.ErrChatHostQueued
		}
		return reserved, err
	})
	mgr.SetChatHostWakeRelease(func(id domain.SessionID) {
		budget.ReleaseIfNoPID(string(id))
	})
}

// reclaimChatHostsUnderPressure frees warm slots when memory pressure or a
// blocked wake queue requires capacity. Orchestrators are never auto-stopped.
// Session workers hibernate first; review/side/background hosts (which count
// toward MaxWorker/MaxTotal but cannot HibernateChat) are shut down one per
// sweep so a soft-queued orchestrator or worker can Drain.
func reclaimChatHostsUnderPressure(
	ctx context.Context,
	budget *chathostbudget.Budget,
	hibernator pressureHibernator,
	dataDir string,
	log *slog.Logger,
) {
	if budget == nil || hibernator == nil {
		return
	}
	if !budget.ShouldReclaim() {
		return
	}
	for _, hostID := range budget.WorkerHostIDs() {
		if ctx.Err() != nil {
			return
		}
		if chathostbudget.ClassifyHostID(hostID) != chathostbudget.ClassWorker {
			continue
		}
		stopped, err := hibernator.HibernateChatForPressure(ctx, domain.SessionID(hostID))
		if err != nil && log != nil && ctx.Err() == nil {
			log.Warn("pressure chat-host reclaim failed", "hostID", hostID, "err", err)
			continue
		}
		if stopped {
			if log != nil {
				log.Info("pressure reclaim hibernated chat host", "hostID", hostID)
			}
			return
		}
	}
	if dataDir == "" {
		return
	}
	for _, hostID := range budget.WorkerHostIDs() {
		if ctx.Err() != nil {
			return
		}
		switch chathostbudget.ClassifyHostID(hostID) {
		case chathostbudget.ClassReview, chathostbudget.ClassSide, chathostbudget.ClassBackground:
		default:
			continue
		}
		if err := persistenthost.Shutdown(ctx, dataDir, hostID); err != nil {
			if log != nil && ctx.Err() == nil {
				log.Warn("pressure reclaim aux host shutdown failed", "hostID", hostID, "err", err)
			}
			continue
		}
		budget.ForceRelease(hostID)
		if log != nil {
			log.Info("pressure reclaim shut down aux chat host", "hostID", hostID)
		}
		return
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
				// Soft capacity deferral and other transient failures stay on the
				// fair queue. Permanent miss (not found / terminated) would loop.
				if shouldRequeueWake(err) {
					budget.Enqueue(w)
					if log != nil {
						log.Info("queued chat host wake failed; requeued", "hostID", w.HostID, "err", err.Error())
					}
					return
				}
				if log != nil {
					log.Info("queued chat host wake dropped", "hostID", w.HostID, "err", err.Error())
				}
			}
		}(item)
	}
}

func shouldRequeueWake(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sessionmanager.ErrNotFound) ||
		errors.Is(err, sessionmanager.ErrTerminated) ||
		errors.Is(err, sessionmanager.ErrNotResumable) {
		return false
	}
	// ErrChatHostQueued means SoftAdmit deferred again — keep fair-queueing.
	return true
}
