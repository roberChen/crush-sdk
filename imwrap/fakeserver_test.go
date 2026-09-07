package imwrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	crush "github.com/roberChen/crush-sdk"
	"github.com/roberChen/crush-sdk/config"
	"github.com/roberChen/crush-sdk/proto"
	"github.com/roberChen/crush-sdk/pubsub"
	"github.com/stretchr/testify/require"
)

// fakeAdapter records everything the wrapper sends to the IM.
type fakeAdapter struct {
	mu    sync.Mutex
	texts []string
	files []fakeFile
}

type fakeFile struct {
	chatID   string
	filename string
	content  string
}

func (f *fakeAdapter) SendText(_ context.Context, chatID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, chatID+"|"+text)
	return nil
}

func (f *fakeAdapter) SendFile(_ context.Context, chatID, filename string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files = append(f.files, fakeFile{chatID: chatID, filename: filename, content: string(content)})
	return nil
}

func (f *fakeAdapter) snapshot() (texts []string, files []fakeFile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.texts...), append([]fakeFile{}, f.files...)
}

func (f *fakeAdapter) findTexts(t *testing.T, substr string) []string {
	t.Helper()
	texts, _ := f.snapshot()
	var out []string
	for _, s := range texts {
		if strings.Contains(s, substr) {
			out = append(out, s)
		}
	}
	return out
}

// fakeServer is a minimal multi-workspace crush server: a
// workspace/session/message store plus a push-able SSE stream per
// workspace, recording the requests the wrapper makes.
type fakeServer struct {
	t  *testing.T
	mu sync.Mutex

	nextWS  int
	nextSes int

	workspaces map[string]*proto.Workspace
	sessions   map[string]map[string]*proto.Session // wsID -> sessionID -> session
	messages   map[string]map[string][]proto.Message

	cfg ModelsConfig

	agentMsgs   []recordedAgent
	grants      []proto.PermissionGrant
	answers     []proto.QuestionAnswer
	modelSets   []recordedModelSet
	cancels     int
	agentCancel int

	push chan string
	srv  *httptest.Server
}

type recordedAgent struct {
	wsID string
	msg  proto.AgentMessage
}

type recordedModelSet struct {
	wsID string
	body struct {
		Scope     config.Scope             `json:"scope"`
		ModelType config.SelectedModelType `json:"model_type"`
		Model     config.SelectedModel     `json:"model"`
	}
}

// ModelsConfig shapes the fake server's /config response.
type ModelsConfig struct {
	Current config.SelectedModel
	// Providers maps provider id to its models.
	Providers map[string][]catwalk.Model
}

// agentInfoResponse mirrors proto.AgentInfo for the fake /agent
// endpoint.
type agentInfoResponse struct {
	IsBusy   bool                 `json:"is_busy"`
	IsReady  bool                 `json:"is_ready"`
	Model    catwalk.Model        `json:"model"`
	ModelCfg config.SelectedModel `json:"model_cfg"`
}

// configResponse mirrors the fields of config.Config the wrapper
// reads.
type configResponse struct {
	Models    map[string]config.SelectedModel `json:"models,omitempty"`
	Providers map[string]providerEntry        `json:"providers,omitempty"`
	Options   *config.Options                 `json:"options,omitempty"`
}

type providerEntry struct {
	Name    string          `json:"name,omitempty"`
	Disable bool            `json:"disable,omitempty"`
	Models  []catwalk.Model `json:"models,omitempty"`
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{
		t:          t,
		nextWS:     0,
		nextSes:    0,
		workspaces: make(map[string]*proto.Workspace),
		sessions:   make(map[string]map[string]*proto.Session),
		messages:   make(map[string]map[string][]proto.Message),
		push:       make(chan string, 100),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/workspaces", f.listWorkspaces)
	mux.HandleFunc("POST /v1/workspaces", f.createWorkspace)
	mux.HandleFunc("GET /v1/workspaces/{id}", f.getWorkspace)
	mux.HandleFunc("GET /v1/workspaces/{id}/events", f.events)
	mux.HandleFunc("GET /v1/workspaces/{id}/sessions", f.listSessions)
	mux.HandleFunc("POST /v1/workspaces/{id}/sessions", f.createSession)
	mux.HandleFunc("GET /v1/workspaces/{id}/sessions/{sid}", f.getSession)
	mux.HandleFunc("GET /v1/workspaces/{id}/sessions/{sid}/messages", f.listMessages)
	mux.HandleFunc("POST /v1/workspaces/{id}/agent", f.sendAgent)
	mux.HandleFunc("GET /v1/workspaces/{id}/agent", f.getAgent)
	mux.HandleFunc("POST /v1/workspaces/{id}/agent/sessions/{sid}/cancel", f.cancelAgent)
	mux.HandleFunc("POST /v1/workspaces/{id}/permissions/grant", f.grant)
	mux.HandleFunc("POST /v1/workspaces/{id}/questions/answer", f.answer)
	mux.HandleFunc("POST /v1/workspaces/{id}/questions/cancel", f.cancelQuestion)
	mux.HandleFunc("POST /v1/workspaces/{id}/current-session", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /v1/workspaces/{id}/config", f.getConfig)
	mux.HandleFunc("POST /v1/workspaces/{id}/config/model", f.setModel)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) client(t *testing.T) *crush.Client {
	t.Helper()
	u := f.srv.URL
	host := strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	c, err := crush.NewClient(t.TempDir(), "tcp", host)
	require.NoError(t, err)
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

func (f *fakeServer) listWorkspaces(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]proto.Workspace, 0, len(f.workspaces))
	for _, ws := range f.workspaces {
		out = append(out, *ws)
	}
	slices.SortFunc(out, func(a, b proto.Workspace) int { return strings.Compare(a.ID, b.ID) })
	writeJSON(f.t, w, out)
}

func (f *fakeServer) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var ws proto.Workspace
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&ws))
	f.mu.Lock()
	f.nextWS++
	ws.ID = "ws" + strconv.Itoa(f.nextWS)
	f.workspaces[ws.ID] = &ws
	f.sessions[ws.ID] = make(map[string]*proto.Session)
	f.messages[ws.ID] = make(map[string][]proto.Message)
	f.mu.Unlock()
	writeJSON(f.t, w, ws)
}

func (f *fakeServer) getWorkspace(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws, ok := f.workspaces[r.PathValue("id")]
	if !ok {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(f.t, w, *ws)
}

func (f *fakeServer) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, "\n")
	flusher.Flush()
	for {
		select {
		case line := <-f.push:
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (f *fakeServer) pushSSE(payloadType string, event any) {
	payload, err := json.Marshal(event)
	require.NoError(f.t, err)
	envelope, err := json.Marshal(pubsub.Payload{Type: payloadType, Payload: payload})
	require.NoError(f.t, err)
	f.push <- string(envelope)
}

func (f *fakeServer) pushMessage(evt pubsub.EventType, m proto.Message) {
	f.pushSSE(pubsub.PayloadTypeMessage, pubsub.Event[proto.Message]{Type: evt, Payload: m})
}

func (f *fakeServer) listSessions(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sessions := f.sessions[r.PathValue("id")]
	out := make([]proto.Session, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, *s)
	}
	writeJSON(f.t, w, out)
}

func (f *fakeServer) createSession(w http.ResponseWriter, r *http.Request) {
	var req proto.Session
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&req))
	wsID := r.PathValue("id")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSes++
	s := proto.Session{
		ID:        "sess-" + strconv.Itoa(f.nextSes),
		Title:     req.Title,
		CreatedAt: 1,
		UpdatedAt: 1,
	}
	f.sessions[wsID][s.ID] = &s
	writeJSON(f.t, w, s)
}

func (f *fakeServer) getSession(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[r.PathValue("id")][r.PathValue("sid")]
	if !ok {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(f.t, w, *s)
}

func (f *fakeServer) listMessages(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON(f.t, w, f.messages[r.PathValue("id")][r.PathValue("sid")])
}

func (f *fakeServer) sendAgent(w http.ResponseWriter, r *http.Request) {
	var msg proto.AgentMessage
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&msg))
	f.mu.Lock()
	f.agentMsgs = append(f.agentMsgs, recordedAgent{wsID: r.PathValue("id"), msg: msg})
	f.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func (f *fakeServer) getAgent(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sel := f.cfg.Current
	if len(f.modelSets) > 0 {
		sel = f.modelSets[len(f.modelSets)-1].body.Model
	}
	model := catwalk.Model{ID: sel.Model, Name: "Fake " + sel.Model, ContextWindow: 200_000}
	if sel.Model == "" {
		model = catwalk.Model{ID: "default", Name: "Default", ContextWindow: 128_000}
	}
	writeJSON(f.t, w, agentInfoResponse{IsReady: true, Model: model, ModelCfg: sel})
}

func (f *fakeServer) cancelAgent(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	f.agentCancel++
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (f *fakeServer) grant(w http.ResponseWriter, r *http.Request) {
	var g proto.PermissionGrant
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&g))
	f.mu.Lock()
	f.grants = append(f.grants, g)
	f.mu.Unlock()
	writeJSON(f.t, w, proto.PermissionGrantResponse{Resolved: true})
}

func (f *fakeServer) answer(w http.ResponseWriter, r *http.Request) {
	var a proto.QuestionAnswer
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&a))
	f.mu.Lock()
	f.answers = append(f.answers, a)
	f.mu.Unlock()
	writeJSON(f.t, w, proto.QuestionAnswerResponse{Resolved: true})
}

func (f *fakeServer) cancelQuestion(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	f.cancels++
	f.mu.Unlock()
	writeJSON(f.t, w, proto.QuestionAnswerResponse{Resolved: true})
}

func (f *fakeServer) getConfig(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := configResponse{}
	if f.cfg.Current.Provider != "" || f.cfg.Current.Model != "" {
		cfg.Models = map[string]config.SelectedModel{"large": f.cfg.Current}
	}
	if len(f.cfg.Providers) > 0 {
		cfg.Providers = make(map[string]providerEntry)
		for id, models := range f.cfg.Providers {
			e := providerEntry{Name: id}
			e.Models = append(e.Models, models...)
			cfg.Providers[id] = e
		}
	}
	writeJSON(f.t, w, cfg)
}

func (f *fakeServer) setModel(w http.ResponseWriter, r *http.Request) {
	var rec recordedModelSet
	rec.wsID = r.PathValue("id")
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&rec.body))
	f.mu.Lock()
	f.modelSets = append(f.modelSets, rec)
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// agentMessages snapshots the recorded SendMessage calls.
func (f *fakeServer) agentMessages() []recordedAgent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedAgent{}, f.agentMsgs...)
}

// setSession mutates a stored session under the fake's lock.
func (f *fakeServer) setSession(wsID, sid string, fn func(*proto.Session)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[wsID][sid]; ok {
		fn(s)
	}
}

// setMessages replaces a session's stored messages.
func (f *fakeServer) setMessages(wsID, sid string, msgs []proto.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages[wsID][sid] = msgs
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
