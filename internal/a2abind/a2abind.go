// Package a2abind lets an unmodified A2A client on this machine talk to one
// enrolled AgentNet peer. It is a local loopback adapter: A2A requests become
// ordinary end-to-end-encrypted AgentNet messages (relay or direct), and A2A
// tasks are views of those messages and their replies. It is not a public
// A2A server and does not make the peer itself speak A2A.
package a2abind

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

// SchemeName is the bearer security scheme declared in the Agent Card.
const SchemeName = "agentnetLocal"

// KindKey is the message metadata key choosing question (default), task or message.
const KindKey = "agentnet.kind"

// Adapter serves the A2A HTTP+JSON binding for one fixed peer.
type Adapter struct {
	agent *client.Agent
	peer  string
	token string
	card  *a2a.AgentCard
}

// New returns an adapter for peer, reachable at baseURL, that requires token
// as a bearer credential on every request, including the Agent Card.
func New(agent *client.Agent, peer, baseURL, token string) *Adapter {
	card := &a2a.AgentCard{
		Name: "AgentNet peer " + peer,
		Description: "Local A2A access to the AgentNet agent " + peer + ". Requests travel as end-to-end " +
			"encrypted AgentNet messages from " + agent.Address + ". Tasks run on the peer only after its user accepts them.",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(baseURL, a2a.TransportProtocolHTTPJSON)},
		DefaultInputModes:   []string{"text/plain"},
		DefaultOutputModes:  []string{"text/plain"},
		Capabilities:        a2a.AgentCapabilities{},
		SecuritySchemes: a2a.NamedSecuritySchemes{
			SchemeName: a2a.HTTPAuthSecurityScheme{Scheme: "Bearer", Description: "token in the local agent home (a2a-token)"},
		},
		SecurityRequirements: a2a.SecurityRequirementsOptions{{SchemeName: {}}},
		Skills: []a2a.AgentSkill{
			{ID: "question", Name: "Ask", Description: "Ask a question; the peer may answer automatically if it approved this sender.", Tags: []string{"question"}},
			{ID: "task", Name: "Task", Description: "Send a task (metadata agentnet.kind=task); it runs only after the peer's user accepts it.", Tags: []string{"task"}},
		},
	}
	return &Adapter{agent: agent, peer: peer, token: token, card: card}
}

// Handler serves the Agent Card and the A2A REST methods, all behind the
// bearer token.
func (ad *Adapter) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(ad.card))
	mux.Handle("/", a2asrv.NewRESTHandler(ad))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, prefix) || subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(ad.token)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"code": http.StatusUnauthorized, "status": "UNAUTHENTICATED", "message": "missing or wrong local AgentNet A2A token"}})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

var _ a2asrv.RequestHandler = (*Adapter)(nil)

// SendMessage sends the A2A message to the peer and returns its task at
// once. While no reply has arrived the task is SUBMITTED, whatever the
// transport has achieved: custody or delivery is not acceptance.
func (ad *Adapter) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	m := req.Message
	if m == nil {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "message is required")
	}
	if m.TaskID != "" || len(m.ReferenceTasks) > 0 {
		return nil, a2a.NewError(a2a.ErrUnsupportedOperation, "each message starts a new task; continuing a task is not supported")
	}
	kind := envelope.KindQuestion
	if k, ok := m.Metadata[KindKey].(string); ok && k != "" {
		kind = k
	}
	if kind != envelope.KindQuestion && kind != envelope.KindTask && kind != envelope.KindMessage {
		return nil, a2a.NewError(a2a.ErrInvalidParams, KindKey+" must be question, task or message")
	}
	var text []string
	var files []string
	for _, p := range m.Parts {
		switch c := p.Content.(type) {
		case a2a.Text:
			text = append(text, string(c))
		case a2a.URL:
			path, err := localFile(string(c))
			if err != nil {
				return nil, a2a.NewError(a2a.ErrUnsupportedContentType, err.Error())
			}
			files = append(files, path)
		default:
			return nil, a2a.NewError(a2a.ErrUnsupportedContentType, "only text parts and file:// URL parts are supported")
		}
	}
	res, err := ad.agent.SendMessage(ctx, client.Outgoing{To: ad.peer, Body: strings.Join(text, "\n"), Files: files, Kind: kind})
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "send failed: "+err.Error())
	}
	return ad.task(res.ID)
}

// localFile accepts only file:// URLs naming an existing regular file; the
// adapter never fetches remote URLs.
func localFile(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return "", errors.New("file parts must be file:// URLs on this machine")
	}
	path := filepath.FromSlash(u.Path)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a readable regular file", path)
	}
	return path, nil
}

// GetTask reports the durable local view of a message sent to this peer.
func (ad *Adapter) GetTask(ctx context.Context, req *a2a.GetTaskRequest) (*a2a.Task, error) {
	return ad.task(string(req.ID))
}

func (ad *Adapter) task(id string) (*a2a.Task, error) {
	s, err := ad.agent.SentMessage(id)
	if errors.Is(err, client.ErrNotSent) || (err == nil && s.To != ad.peer) {
		return nil, a2a.ErrTaskNotFound // includes messages to other peers
	}
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, err.Error())
	}
	return project(s), nil
}

// project maps an AgentNet message and its reply to an A2A task.
func project(s client.Sent) *a2a.Task {
	sent := &a2a.Message{ID: s.ID, ContextID: s.ID, TaskID: a2a.TaskID(s.ID), Role: a2a.MessageRoleUser,
		Parts: a2a.ContentParts{a2a.NewTextPart(s.Body)}}
	ts := s.CreatedAt
	t := &a2a.Task{ID: a2a.TaskID(s.ID), ContextID: s.ID, History: []*a2a.Message{sent},
		Metadata: map[string]any{KindKey: s.Kind, "agentnet.delivery": s.State, "agentnet.path": s.Path}}
	note := func(text string) *a2a.Message {
		return &a2a.Message{ID: s.ID + "-status", ContextID: s.ID, TaskID: a2a.TaskID(s.ID), Role: a2a.MessageRoleAgent,
			Parts: a2a.ContentParts{a2a.NewTextPart(text)}}
	}
	if s.Reply == nil {
		switch s.State {
		case "failed":
			t.Status = a2a.TaskStatus{State: a2a.TaskStateFailed, Message: note("AgentNet could not deliver the message"), Timestamp: &ts}
		case "expired":
			t.Status = a2a.TaskStatus{State: a2a.TaskStateFailed, Message: note("the addressed session ended before delivery"), Timestamp: &ts}
		default:
			t.Status = a2a.TaskStatus{State: a2a.TaskStateSubmitted,
				Message: note("sent; AgentNet delivery state: " + s.State + ". No reply yet."), Timestamp: &ts}
		}
		return t
	}
	r := s.Reply
	parts := a2a.ContentParts{a2a.NewTextPart(r.Body)}
	for _, f := range r.Attachments {
		p := a2a.NewFileURLPart(a2a.URL("agentnet://attachment/"+r.ID+"/"+f.BlobID), "application/octet-stream")
		p.Filename = f.Name
		parts = append(parts, p)
	}
	t.Artifacts = []*a2a.Artifact{{ID: a2a.ArtifactID(r.ID), Name: r.Kind, Parts: parts,
		Description: "reply from " + r.From + "; attachments: `agentnet download " + r.ID + "`"}}
	state := a2a.TaskStateCompleted
	switch r.Status {
	case envelope.StatusFailed, envelope.StatusTimeout, envelope.StatusInterrupted:
		state = a2a.TaskStateFailed
	case envelope.StatusCancelled:
		state = a2a.TaskStateCanceled
	case envelope.StatusDeclined:
		state = a2a.TaskStateRejected
	}
	at := r.ReceivedAt
	t.Status = a2a.TaskStatus{State: state, Timestamp: &at}
	return t
}

// ListTasks lists messages sent to this peer, newest first.
func (ad *Adapter) ListTasks(ctx context.Context, req *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	size := req.PageSize
	if size <= 0 || size > 100 {
		size = 50
	}
	ids, err := ad.agent.SentTo(ad.peer, size)
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, err.Error())
	}
	out := &a2a.ListTasksResponse{Tasks: []*a2a.Task{}, PageSize: size}
	for _, id := range ids {
		t, err := ad.task(id)
		if err != nil {
			continue
		}
		if req.Status != "" && t.Status.State != req.Status {
			continue
		}
		out.Tasks = append(out.Tasks, t)
	}
	out.TotalSize = len(out.Tasks)
	return out, nil
}

// CancelTask is refused: AgentNet has no way to stop work on the peer, and
// a queued request would not prove it stopped. The peer's user can decline.
func (ad *Adapter) CancelTask(ctx context.Context, req *a2a.CancelTaskRequest) (*a2a.Task, error) {
	if _, err := ad.task(string(req.ID)); err != nil {
		return nil, err
	}
	return nil, a2a.NewError(a2a.ErrTaskNotCancelable, "AgentNet cannot stop work on the peer; its user may decline or cancel locally")
}

func noEvents(err error) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) { yield(nil, err) }
}

// SubscribeToTask is not supported (the card advertises no streaming).
func (ad *Adapter) SubscribeToTask(context.Context, *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return noEvents(a2a.ErrUnsupportedOperation)
}

// SendStreamingMessage is not supported (the card advertises no streaming).
func (ad *Adapter) SendStreamingMessage(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return noEvents(a2a.ErrUnsupportedOperation)
}

// GetTaskPushConfig is not supported.
func (ad *Adapter) GetTaskPushConfig(context.Context, *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	return nil, a2a.ErrPushNotificationNotSupported
}

// ListTaskPushConfigs is not supported.
func (ad *Adapter) ListTaskPushConfigs(context.Context, *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	return nil, a2a.ErrPushNotificationNotSupported
}

// CreateTaskPushConfig is not supported.
func (ad *Adapter) CreateTaskPushConfig(context.Context, *a2a.PushConfig) (*a2a.PushConfig, error) {
	return nil, a2a.ErrPushNotificationNotSupported
}

// DeleteTaskPushConfig is not supported.
func (ad *Adapter) DeleteTaskPushConfig(context.Context, *a2a.DeleteTaskPushConfigRequest) error {
	return a2a.ErrPushNotificationNotSupported
}

// GetExtendedAgentCard is not supported.
func (ad *Adapter) GetExtendedAgentCard(context.Context, *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	return nil, a2a.ErrExtendedCardNotConfigured
}
