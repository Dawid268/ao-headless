package projectxfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ChatAdapter bridges durable conversation APIs onto ConversationReader.
type ChatAdapter struct {
	ProjectConv func(ctx context.Context, project domain.ProjectID) (domain.ConversationRecord, error)
	SessionConv func(ctx context.Context, session domain.SessionID) (domain.ConversationRecord, error)
	LoadSnap    func(ctx context.Context, conversationID string) (messages []domain.ConversationMessage, activities []domain.ConversationActivity, err error)
	NotFound    error
}

// ProjectConversation implements ConversationReader.
func (a ChatAdapter) ProjectConversation(ctx context.Context, project domain.ProjectID) (domain.ConversationRecord, bool, error) {
	rec, err := a.ProjectConv(ctx, project)
	if err != nil {
		if a.isNotFound(err) {
			return domain.ConversationRecord{}, false, nil
		}
		return domain.ConversationRecord{}, false, err
	}
	return rec, true, nil
}

// ConversationForSession implements ConversationReader.
func (a ChatAdapter) ConversationForSession(ctx context.Context, session domain.SessionID) (domain.ConversationRecord, bool, error) {
	rec, err := a.SessionConv(ctx, session)
	if err != nil {
		if a.isNotFound(err) {
			return domain.ConversationRecord{}, false, nil
		}
		return domain.ConversationRecord{}, false, err
	}
	return rec, true, nil
}

// LoadConversationMessages implements ConversationReader.
func (a ChatAdapter) LoadConversationMessages(ctx context.Context, conversationID string) ([]ExportedMessage, []string, error) {
	if a.LoadSnap == nil {
		return nil, nil, fmt.Errorf("conversation snapshot loader not configured")
	}
	msgs, acts, err := a.LoadSnap(ctx, conversationID)
	if err != nil {
		return nil, nil, err
	}
	out := make([]ExportedMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Streaming {
			continue
		}
		out = append(out, ExportedMessage{
			Role:      string(m.Role),
			Origin:    string(m.Origin),
			Text:      m.Text,
			CreatedAt: m.CreatedAt,
		})
	}
	actText := make([]string, 0, len(acts))
	for _, act := range acts {
		if act.Summary == "" {
			continue
		}
		actText = append(actText, fmt.Sprintf("%s: %s", act.Kind, act.Summary))
	}
	return out, actText, nil
}

func (a ChatAdapter) isNotFound(err error) bool {
	if a.NotFound != nil && errors.Is(err, a.NotFound) {
		return true
	}
	return errors.Is(err, domain.ErrNoConversation)
}
