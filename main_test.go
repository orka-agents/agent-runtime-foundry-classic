package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orka-agents/agent-runtime-foundry-classic/internal/harness"
)

const (
	foundryStatusRequiresAction = "requires_action"
	foundryStatusCompleted      = "completed"
	foundryStatusCancelled      = "cancelled"
)

type staticFoundryTokenProvider string

func (p staticFoundryTokenProvider) AccessToken(context.Context) (string, error) {
	return string(p), nil
}

func TestFoundryAdapterObservedTurnCompletes(t *testing.T) {
	foundry := newFakeFoundry(t, foundryStatusCompleted)
	adapter := newTestFoundryAdapter(foundry.URL)
	client := newHarnessClient(t, adapter)

	request := foundryStartTurnRequest("foundry-observed")
	if _, err := client.StartTurn(context.Background(), request); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	var frames []harness.HarnessEventFrame
	if err := client.StreamFrames(context.Background(), request.TurnID, 0, func(frame harness.HarnessEventFrame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("StreamFrames: %v", err)
	}
	if !hasFrameType(frames, harness.FrameTurnStarted) || !hasFrameType(frames, harness.FrameTurnCompleted) {
		t.Fatalf("frames = %#v, want started and completed", frames)
	}
	if got := frames[len(frames)-1].Completed.Result; got != "foundry final answer" {
		t.Fatalf("result = %q", got)
	}
	if !foundry.sawEmptyTools.Load() {
		t.Fatalf("observed Foundry run did not disable persisted Foundry tools with tools=[]")
	}
}

func TestFoundryAdapterBrokeredReadContinuation(t *testing.T) {
	testFoundryAdapterBrokeredContinuation(t, harness.BrokeredToolClassRead)
}

func TestFoundryAdapterBrokeredWriteContinuation(t *testing.T) {
	testFoundryAdapterBrokeredContinuation(t, harness.BrokeredToolClassWrite)
}

func testFoundryAdapterBrokeredContinuation(t *testing.T, class harness.BrokeredToolClass) {
	foundry := newFakeFoundry(t, foundryStatusRequiresAction)
	adapter := newTestFoundryAdapter(foundry.URL)
	client := newHarnessClient(t, adapter)

	request := foundryStartTurnRequest("foundry-brokered-" + string(class))
	request.ToolExecutionMode = harness.ToolExecutionModeBrokered
	request.Input.Tools = []harness.ToolDefinition{{
		Name:          "support-ticket-lookup",
		Description:   "Look up support ticket",
		BrokeredClass: class,
		Parameters:    json.RawMessage(`{"type":"object","properties":{"incident":{"type":"string"}}}`),
	}}
	if _, err := client.StartTurn(context.Background(), request); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	var frames []harness.HarnessEventFrame
	if err := client.StreamFrames(context.Background(), request.TurnID, 0, func(frame harness.HarnessEventFrame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("StreamFrames before continue: %v", err)
	}
	requested := findFrame(frames, harness.FrameToolCallRequested)
	if requested == nil {
		t.Fatalf("frames = %#v, want tool request", frames)
	}
	if requested.ToolName != "support-ticket-lookup" || requested.ToolCallID != "call-1" {
		t.Fatalf("tool request = %#v", requested)
	}
	if !foundry.sawSafeToolSchema.Load() {
		t.Fatalf("Foundry run did not receive safe Orka tool schema")
	}
	_, err := client.ContinueTurn(context.Background(), harness.ContinueTurnRequest{
		Version:          harness.ProtocolVersion,
		Namespace:        request.Namespace,
		TaskName:         request.TaskName,
		SessionName:      request.SessionName,
		RuntimeSessionID: request.RuntimeSessionID,
		TurnID:           request.TurnID,
		CorrelationID:    request.CorrelationID,
		ToolResults: []harness.ToolCallResult{{
			Version:          harness.ProtocolVersion,
			RuntimeSessionID: request.RuntimeSessionID,
			TurnID:           request.TurnID,
			ToolCallID:       requested.ToolCallID,
			IdempotencyKey:   harness.ToolRequestIdempotencyKey(request.RuntimeSessionID, request.TurnID, requested.ToolCallID),
			Approved:         true,
			Output:           json.RawMessage(`{"success":true,"data":{"status":"ok"}}`),
		}},
	})
	if err != nil {
		t.Fatalf("ContinueTurn: %v", err)
	}
	frames = nil
	if err := client.StreamFrames(context.Background(), request.TurnID, requested.Seq, func(frame harness.HarnessEventFrame) error { //nolint:lll
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("StreamFrames after continue: %v", err)
	}
	if !hasFrameType(frames, harness.FrameToolResultReceived) || !hasFrameType(frames, harness.FrameTurnCompleted) {
		t.Fatalf("frames = %#v, want tool result and completion", frames)
	}
	if foundry.submittedToolOutput.Load() != 1 {
		t.Fatalf("submitted tool outputs = %d, want 1", foundry.submittedToolOutput.Load())
	}
}

func TestFoundryEndpointIsSafe(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{name: "https", endpoint: "https://example.openai.azure.com", want: true},
		{name: "https with path", endpoint: "https://example.openai.azure.com/openai/assistants", want: true},
		{name: "http localhost", endpoint: "http://localhost:8080", want: true},
		{name: "http ipv4 loopback", endpoint: "http://127.0.0.1:8080", want: true},
		{name: "http ipv6 loopback", endpoint: "http://[::1]:8080", want: true},
		{name: "http remote", endpoint: "http://example.openai.azure.com", want: false},
		{name: "https userinfo", endpoint: "https://user@example.openai.azure.com", want: false},
		{name: "https query", endpoint: "https://example.openai.azure.com?api-version=v1", want: false},
		{name: "https bare query", endpoint: "https://example.openai.azure.com?", want: false},
		{name: "https fragment", endpoint: "https://example.openai.azure.com#frag", want: false},
		{name: "https bare fragment", endpoint: "https://example.openai.azure.com#", want: false},
		{name: "missing scheme", endpoint: "example.openai.azure.com", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := foundryEndpointIsSafe(tt.endpoint); got != tt.want {
				t.Fatalf("foundryEndpointIsSafe(%q) = %v, want %v", tt.endpoint, got, tt.want)
			}
		})
	}
}

func TestFoundryURLRejectsUnsafeEndpoint(t *testing.T) {
	s := &server{cfg: config{endpoint: "http://example.openai.azure.com", apiVersion: "v1"}}
	if _, err := s.foundryURL("/threads"); err == nil {
		t.Fatalf("foundryURL accepted unsafe endpoint")
	}
}

type fakeFoundry struct {
	*httptest.Server
	status               string
	cancelCalls          atomic.Int32
	cancelPolls          atomic.Int32
	deleteThreadCalls    atomic.Int32
	submittedToolOutput  atomic.Int32
	sawSafeToolSchema    atomic.Bool
	sawEmptyTools        atomic.Bool
	toolName             atomic.Value
	forcedToolName       atomic.Value
	cancelResponseStatus string
}

func newFakeFoundry(t *testing.T, status string) *fakeFoundry {
	t.Helper()
	f := &fakeFoundry{status: status}
	mux := http.NewServeMux()
	mux.HandleFunc("/threads", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]string{"id": "thread-1"})
	})
	mux.HandleFunc("/threads/thread-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		f.deleteThreadCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/threads/thread-1/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if tools, ok := body["tools"].([]any); ok && len(tools) == 0 {
			f.sawEmptyTools.Store(true)
		}
		if tools, ok := body["tools"].([]any); ok && len(tools) == 1 {
			encoded, _ := json.Marshal(tools[0])
			text := string(encoded)
			if !strings.Contains(text, "http://") && !strings.Contains(text, "Secret") {
				f.sawSafeToolSchema.Store(true)
			}
			if tool, ok := tools[0].(map[string]any); ok {
				if fn, ok := tool["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok {
						f.toolName.Store(name)
					}
				}
			}
		}
		writeJSON(w, map[string]string{"id": "run-1", "status": f.status})
	})
	mux.HandleFunc("/threads/thread-1/runs/run-1", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if f.cancelCalls.Load() > 0 && f.cancelResponseStatus == "cancelling" {
				if f.cancelPolls.Add(1) < 2 {
					writeJSON(w, map[string]string{"id": "run-1", "status": "cancelling"})
				} else {
					writeJSON(w, map[string]string{"id": "run-1", "status": foundryStatusCancelled})
				}
				return
			}
			if f.status == foundryStatusRequiresAction && f.submittedToolOutput.Load() == 0 {
				writeJSON(w, map[string]any{"id": "run-1", "status": foundryStatusRequiresAction, "required_action": map[string]any{"submit_tool_outputs": map[string]any{"tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": f.currentToolName(), "arguments": json.RawMessage(`{"incident":"inc-1"}`)}}}}}}) //nolint:lll
				return
			}
			status := f.status
			if status == foundryStatusRequiresAction {
				status = foundryStatusCompleted
			}
			writeJSON(w, map[string]string{"id": "run-1", "status": status})
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/threads/thread-1/runs/run-1/cancel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		f.cancelCalls.Add(1)
		status := f.cancelResponseStatus
		if status == "" {
			status = foundryStatusCancelled
		}
		writeJSON(w, map[string]string{"id": "run-1", "status": status})
	})
	mux.HandleFunc("/threads/thread-1/runs/run-1/submit_tool_outputs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		f.submittedToolOutput.Add(1)
		writeJSON(w, map[string]string{"id": "run-1", "status": "queued"})
	})
	mux.HandleFunc("/threads/thread-1/messages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{map[string]any{"role": "assistant", "content": "foundry final answer"}}})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeFoundry) currentToolName() string {
	if value, ok := f.forcedToolName.Load().(string); ok && value != "" {
		return value
	}
	if value, ok := f.toolName.Load().(string); ok && value != "" {
		return value
	}
	return "support-ticket-lookup"
}

func newTestFoundryAdapter(endpoint string) *httptest.Server {
	return newTestFoundryAdapterWithTiming(endpoint, time.Second, time.Millisecond)
}

func newTestFoundryAdapterWithTiming(endpoint string, pollTimeout, pollInterval time.Duration) *httptest.Server {
	s := &server{cfg: config{addr: ":0", runtimeName: "foundry-test", adapterBearer: "adapter-token", endpoint: endpoint, agentID: "agent-1", apiVersion: "", pollTimeout: pollTimeout, pollInterval: pollInterval}, client: newFoundryHTTPClient(endpoint), tokenProvider: staticFoundryTokenProvider("mock-token"), turns: map[harness.HarnessTurnID]*turnState{}, runtimeThreads: map[harness.RuntimeSessionID]string{}, runtimeThreadSeen: map[harness.RuntimeSessionID]time.Time{}, turnTombstones: map[harness.HarnessTurnID]turnTombstone{}} //nolint:lll
	return httptest.NewServer(s.handler())
}

func newHarnessClient(t *testing.T, adapter *httptest.Server) *harness.Client {
	t.Helper()
	t.Cleanup(adapter.Close)
	client, err := harness.NewClient(adapter.URL, harness.WithBearerToken("adapter-token"), harness.WithControlTimeout(2*time.Second)) //nolint:lll
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func foundryStartTurnRequest(name string) harness.StartTurnRequest {
	return harness.StartTurnRequest{Version: harness.ProtocolVersion, Namespace: "default", TaskName: name, SessionName: name, RuntimeSessionID: harness.RuntimeSessionID(name + "-runtime"), TurnID: harness.HarnessTurnID(name + "-turn"), CorrelationID: name + "-corr", Deadline: time.Now().UTC().Add(time.Minute), AuthIdentity: harness.AuthIdentity{Subject: "task:default/" + name}, ToolExecutionMode: harness.ToolExecutionModeObserved, Input: harness.TurnInput{Prompt: "Investigate incident"}} //nolint:lll
}

func hasFrameType(frames []harness.HarnessEventFrame, typ harness.FrameType) bool {
	return findFrame(frames, typ) != nil
}
func findFrame(frames []harness.HarnessEventFrame, typ harness.FrameType) *harness.HarnessEventFrame {
	for i := range frames {
		if frames[i].Type == typ {
			return &frames[i]
		}
	}
	return nil
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestFoundryAdapterPollTimeoutFailsAndCancels(t *testing.T) {
	foundry := newFakeFoundry(t, "queued")
	adapter := newTestFoundryAdapterWithTiming(foundry.URL, 25*time.Millisecond, time.Millisecond)
	client := newHarnessClient(t, adapter)
	request := foundryStartTurnRequest("foundry-timeout")
	if _, err := client.StartTurn(context.Background(), request); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	var frames []harness.HarnessEventFrame
	if err := client.StreamFrames(context.Background(), request.TurnID, 0, func(frame harness.HarnessEventFrame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("StreamFrames: %v", err)
	}
	failed := findFrame(frames, harness.FrameTurnFailed)
	if failed == nil || failed.Failed == nil || failed.Failed.Reason != "foundry_poll_timeout" {
		t.Fatalf("frames = %#v, want foundry_poll_timeout", frames)
	}
	if foundry.cancelCalls.Load() != 1 {
		t.Fatalf("cancel calls = %d, want 1", foundry.cancelCalls.Load())
	}
}

func TestFoundryAdapterRejectsMismatchedCancelBeforeSideEffect(t *testing.T) {
	foundry := newFakeFoundry(t, "queued")
	adapter := newTestFoundryAdapter(foundry.URL)
	defer adapter.Close()
	client, err := harness.NewClient(adapter.URL, harness.WithBearerToken("adapter-token"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	request := foundryStartTurnRequest("foundry-cancel")
	if _, err := client.StartTurn(context.Background(), request); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	cancelRequest := harness.CancelTurnRequest{
		Version:          harness.ProtocolVersion,
		Namespace:        request.Namespace,
		TaskName:         request.TaskName,
		SessionName:      request.SessionName,
		RuntimeSessionID: request.RuntimeSessionID,
		TurnID:           request.TurnID,
		CorrelationID:    "wrong-correlation",
	}
	payload, err := json.Marshal(cancelRequest)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	cancelPath, err := harness.CancelTurnPath(request.TurnID)
	if err != nil {
		t.Fatalf("CancelTurnPath: %v", err)
	}
	httpRequest, err := http.NewRequest(http.MethodPost, adapter.URL+cancelPath, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer adapter-token")
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer response.Body.Close() //nolint:errcheck
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("cancel status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	if foundry.cancelCalls.Load() != 0 {
		t.Fatalf("cancel calls = %d, want 0", foundry.cancelCalls.Load())
	}
}

func TestFoundryHTTPClientRejectsCrossOriginRedirect(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	s := &server{
		cfg:           config{endpoint: source.URL},
		client:        newFoundryHTTPClient(source.URL),
		tokenProvider: staticFoundryTokenProvider("mock-token"),
	}
	err := s.doJSON(
		context.Background(), http.MethodPost, "/threads", map[string]string{"prompt": "safe"}, nil,
	)
	if err == nil {
		t.Fatal("doJSON() error = nil, want cross-origin redirect rejection")
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests.Load())
	}
}

func TestFoundryHTTPClientAllowsSameOriginRedirect(t *testing.T) {
	var sawKey atomic.Bool
	mux := http.NewServeMux()
	backend := httptest.NewServer(mux)
	defer backend.Close()
	mux.HandleFunc("/threads", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		sawKey.Store(r.Header.Get("Authorization") == "Bearer mock-token")
		writeJSON(w, map[string]string{"id": "thread-1"})
	})

	s := &server{
		cfg:           config{endpoint: backend.URL},
		client:        newFoundryHTTPClient(backend.URL),
		tokenProvider: staticFoundryTokenProvider("mock-token"),
	}
	var out foundryCreateThreadResponse
	if err := s.doJSON(
		context.Background(), http.MethodPost, "/threads", map[string]string{"prompt": "safe"}, &out,
	); err != nil {
		t.Fatalf("doJSON() error = %v", err)
	}
	if out.ID != "thread-1" || !sawKey.Load() {
		t.Fatalf("response = %#v sawKey=%t", out, sawKey.Load())
	}
}

func TestFoundryAdapterCancelsRejectedBrokeredTool(t *testing.T) {
	foundry := newFakeFoundry(t, foundryStatusRequiresAction)
	foundry.forcedToolName.Store("wrong-tool")
	adapter := newTestFoundryAdapter(foundry.URL)
	client := newHarnessClient(t, adapter)
	request := foundryStartTurnRequest("foundry-wrong-tool")
	request.ToolExecutionMode = harness.ToolExecutionModeBrokered
	request.Input.Tools = []harness.ToolDefinition{{
		Name:          "support-ticket-lookup",
		BrokeredClass: harness.BrokeredToolClassRead,
		Parameters:    json.RawMessage(`{"type":"object"}`),
	}}
	if _, err := client.StartTurn(context.Background(), request); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	var frames []harness.HarnessEventFrame
	if err := client.StreamFrames(context.Background(), request.TurnID, 0, func(frame harness.HarnessEventFrame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("StreamFrames: %v", err)
	}
	failed := findFrame(frames, harness.FrameTurnFailed)
	if failed == nil || failed.Failed == nil || failed.Failed.Reason != "foundry_poll_failed" {
		t.Fatalf("frames = %#v, want foundry_poll_failed", frames)
	}
	if foundry.cancelCalls.Load() != 1 {
		t.Fatalf("cancel calls = %d, want 1", foundry.cancelCalls.Load())
	}
}

func TestPrepareFoundryStartRequestDropsTurnEnvironment(t *testing.T) {
	request := foundryStartTurnRequest("foundry-env")
	request.Input.Env = []harness.TurnEnvVar{{Name: "PROVIDER_TOKEN", Value: "mock-token"}}
	prepared := prepareFoundryStartRequest(request)
	if len(prepared.Input.Env) != 0 {
		t.Fatalf("prepared env = %#v, want empty", prepared.Input.Env)
	}
	if request.Input.Env[0].Value != "" {
		t.Fatal("prepareFoundryStartRequest did not scrub the source backing slice")
	}
}

func TestFoundryAdapterRejectsOversizedBrokeredResultBeforeSubmission(t *testing.T) {
	foundry := newFakeFoundry(t, foundryStatusRequiresAction)
	adapter := newTestFoundryAdapter(foundry.URL)
	client := newHarnessClient(t, adapter)
	request := foundryStartTurnRequest("foundry-large-result")
	request.ToolExecutionMode = harness.ToolExecutionModeBrokered
	request.Input.Tools = []harness.ToolDefinition{{
		Name:          "support-ticket-lookup",
		BrokeredClass: harness.BrokeredToolClassRead,
		Parameters:    json.RawMessage(`{"type":"object"}`),
	}}
	if _, err := client.StartTurn(context.Background(), request); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	var requested *harness.HarnessEventFrame
	if err := client.StreamFrames(context.Background(), request.TurnID, 0, func(frame harness.HarnessEventFrame) error {
		if frame.Type == harness.FrameToolCallRequested {
			copyFrame := frame
			requested = &copyFrame
		}
		return nil
	}); err != nil {
		t.Fatalf("StreamFrames: %v", err)
	}
	if requested == nil {
		t.Fatal("tool request frame not found")
	}
	oversized, err := json.Marshal(strings.Repeat("x", maxFoundryBrokeredFrameBytes))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	_, err = client.ContinueTurn(context.Background(), harness.ContinueTurnRequest{
		Version:          harness.ProtocolVersion,
		Namespace:        request.Namespace,
		TaskName:         request.TaskName,
		SessionName:      request.SessionName,
		RuntimeSessionID: request.RuntimeSessionID,
		TurnID:           request.TurnID,
		CorrelationID:    request.CorrelationID,
		ToolResults: []harness.ToolCallResult{{
			Version:          harness.ProtocolVersion,
			RuntimeSessionID: request.RuntimeSessionID,
			TurnID:           request.TurnID,
			ToolCallID:       requested.ToolCallID,
			IdempotencyKey: harness.ToolRequestIdempotencyKey(
				request.RuntimeSessionID, request.TurnID, requested.ToolCallID,
			),
			Approved: true,
			Output:   oversized,
		}},
	})
	var clientErr harness.ClientError
	if err == nil || !errors.As(err, &clientErr) || clientErr.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("ContinueTurn() error = %v, want status %d", err, http.StatusRequestEntityTooLarge)
	}
	if foundry.submittedToolOutput.Load() != 0 {
		t.Fatalf("submitted tool outputs = %d, want 0", foundry.submittedToolOutput.Load())
	}
}

func TestFoundryAdapterTombstoneRejectsDuplicateExecution(t *testing.T) {
	request := prepareFoundryStartRequest(foundryStartTurnRequest("foundry-tombstone"))
	s := &server{
		cfg:            config{adapterBearer: "adapter-token"},
		turns:          map[harness.HarnessTurnID]*turnState{},
		turnTombstones: map[harness.HarnessTurnID]turnTombstone{},
	}
	s.turnTombstones[request.TurnID] = turnTombstone{
		request:   request,
		expiresAt: time.Now().UTC().Add(time.Hour),
	}
	adapter := httptest.NewServer(s.handler())
	client := newHarnessClient(t, adapter)
	if _, err := client.StartTurn(context.Background(), request); err == nil || !strings.Contains(err.Error(), "(409)") {
		t.Fatalf("StartTurn() error = %v, want tombstone conflict", err)
	}
	if len(s.turns) != 0 {
		t.Fatalf("turns = %d, want no duplicate execution", len(s.turns))
	}
}

func TestCleanupExpiredFoundryThreadsDeletesRemoteThread(t *testing.T) {
	foundry := newFakeFoundry(t, foundryStatusCompleted)
	sessionID := harness.RuntimeSessionID("expired-session")
	s := &server{
		cfg:               config{endpoint: foundry.URL},
		client:            newFoundryHTTPClient(foundry.URL),
		tokenProvider:     staticFoundryTokenProvider("mock-token"),
		turns:             map[harness.HarnessTurnID]*turnState{},
		runtimeThreads:    map[harness.RuntimeSessionID]string{sessionID: "thread-1"},
		runtimeThreadSeen: map[harness.RuntimeSessionID]time.Time{sessionID: time.Now().UTC().Add(-time.Hour)},
	}
	s.cleanupExpiredFoundryThreads(time.Now().UTC())
	if foundry.deleteThreadCalls.Load() != 1 {
		t.Fatalf("delete thread calls = %d, want 1", foundry.deleteThreadCalls.Load())
	}
	if _, exists := s.runtimeThreads[sessionID]; exists {
		t.Fatal("expired runtime thread mapping was retained after successful deletion")
	}
}

func TestFoundryAdapterWaitsForCancellationTerminalStatus(t *testing.T) {
	foundry := newFakeFoundry(t, "queued")
	foundry.cancelResponseStatus = "cancelling"
	adapter := newTestFoundryAdapterWithTiming(foundry.URL, time.Second, time.Millisecond)
	client := newHarnessClient(t, adapter)
	request := foundryStartTurnRequest("foundry-cancel-wait")
	if _, err := client.StartTurn(context.Background(), request); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	_, err := client.CancelTurn(context.Background(), harness.CancelTurnRequest{
		Version:          harness.ProtocolVersion,
		Namespace:        request.Namespace,
		TaskName:         request.TaskName,
		SessionName:      request.SessionName,
		RuntimeSessionID: request.RuntimeSessionID,
		TurnID:           request.TurnID,
		CorrelationID:    request.CorrelationID,
		Reason:           "test cancellation",
	})
	if err != nil {
		t.Fatalf("CancelTurn: %v", err)
	}
	if foundry.cancelPolls.Load() < 2 {
		t.Fatalf("cancel polls = %d, want cancellation confirmation polling", foundry.cancelPolls.Load())
	}
}

func TestAppendFailedPreservesLocalDiagnostic(t *testing.T) {
	s := &server{}
	turn := &turnState{request: foundryStartTurnRequest("local-failure")}
	s.appendFailed(turn, "local_limit", "completion exceeded local limit")
	failed := findFrame(turn.frames, harness.FrameTurnFailed)
	if failed == nil || failed.Failed == nil || failed.Failed.Message != "completion exceeded local limit" {
		t.Fatalf("failed frame = %#v, want local diagnostic", failed)
	}
}

func TestStartTurnPreservesLocalValidationDiagnostic(t *testing.T) {
	foundry := newFakeFoundry(t, foundryStatusCompleted)
	adapter := newTestFoundryAdapter(foundry.URL)
	defer adapter.Close()
	request := foundryStartTurnRequest("local-validation")
	request.ToolExecutionMode = harness.ToolExecutionModeBrokered
	request.Input.Tools = []harness.ToolDefinition{{
		Name:          "delegate_task",
		BrokeredClass: harness.BrokeredToolClassCoordination,
		Parameters:    json.RawMessage(`{"type":"object"}`),
	}}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	httpRequest, err := http.NewRequest(http.MethodPost, adapter.URL+harness.TurnsPath, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer adapter-token")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer response.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(body), "unsupported Foundry brokered tool class") {
		t.Fatalf("response body = %q, want actionable local validation", body)
	}
}
