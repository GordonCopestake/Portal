package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func call(id, command string) toolCall {
	var c toolCall
	c.ID, c.Type, c.Function.Name = id, "function", "powershell_call"
	args, _ := json.Marshal(map[string]string{"command": command})
	c.Function.Arguments = string(args)
	return c
}

func reply(w http.ResponseWriter, m message) {
	reason := "stop"
	if len(m.ToolCalls) > 0 {
		reason = "tool_calls"
	}
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": m, "finish_reason": reason}}})
}

func TestRepairAndRetryDoNotRepeatCommands(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.txt")
	quoted := strings.ReplaceAll(file, "'", "''")
	commands := []toolCall{call("inspect", "Test-Path -LiteralPath '"+quoted+"'"), call("write", "[IO.File]::WriteAllText('"+quoted+"', 'fixed')"), call("verify", "[IO.File]::ReadAllText('"+quoted+"')")}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local" {
			t.Error("incorrect request path or key")
		}
		var request struct {
			Messages []message
			Tools    []json.RawMessage
			Stream   bool
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 1 || !bytes.Equal(request.Tools[0], powershellSchema) || !request.Stream || request.Messages[0].Content != validConfig().SystemPrompt {
			t.Error("prompt or tool schema changed")
		}
		switch requests.Add(1) {
		case 1:
			reply(w, message{Role: "assistant", ToolCalls: commands})
		case 2:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
		case 3:
			if len(request.Messages) != 7 || request.Messages[4].ToolCallID != "inspect" || request.Messages[5].ToolCallID != "write" || request.Messages[6].ToolCallID != "verify" || !strings.Contains(request.Messages[6].Content, "fixed") {
				t.Error("tool results not retained across retry")
			}
			reply(w, message{Role: "assistant", Content: "Verified fixed."})
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	c := validConfig()
	c.BaseURL = server.URL + "/v1"
	c.APIKey = "local"
	c.ApprovalMode = "automatic"
	client := newModelClient(c)
	defer client.close()
	var out bytes.Buffer
	a := newAgent(client, "Test workstation", dir, &out)
	executions := 0
	actual := a.execute
	a.execute = func(ctx context.Context, command string) toolResult { executions++; return actual(ctx, command) }
	if err := a.turn(context.Background(), "Fix the test file and verify it."); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "fixed" || executions != 3 || requests.Load() != 3 || !strings.Contains(out.String(), "Verified fixed") {
		t.Fatalf("repair failed: %v; executions %d; output %s", err, executions, out.String())
	}
}

func TestRejectedBatchesCannotStartCommands(t *testing.T) {
	for _, kind := range []string{"duplicate", "missing ID", "bad type", "budget", "repeated response"} {
		t.Run(kind, func(t *testing.T) {
			calls := []toolCall{call("one", "'one'"), call("two", "'two'")}
			switch kind {
			case "duplicate":
				calls[1].ID = calls[0].ID
			case "missing ID":
				calls[1].ID = ""
			case "bad type":
				calls[1].Type = "other"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reply(w, message{Role: "assistant", ToolCalls: calls}) }))
			defer server.Close()
			c := validConfig()
			c.BaseURL = server.URL
			c.ApprovalMode = "automatic"
			if kind == "budget" {
				c.MaxToolCallsPerTurn = 1
			}
			client := newModelClient(c)
			defer client.close()
			a := newAgent(client, "test", t.TempDir(), io.Discard)
			if kind == "repeated response" {
				a.seen["two"] = true
			}
			a.execute = func(context.Context, string) toolResult { t.Error("command executed"); return toolResult{} }
			if err := a.turn(context.Background(), "test"); err == nil {
				t.Fatal("invalid batch accepted")
			}
		})
	}
}

func TestDenialAndBadArgumentsReturnResults(t *testing.T) {
	calls := []toolCall{call("denied", "'no'"), call("unknown", "'no'"), call("invalid", "'no'")}
	calls[1].Function.Name = "other"
	calls[2].Function.Arguments = `{"command":"'no'","extra":true}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			reply(w, message{Role: "assistant", ToolCalls: calls})
			return
		}
		var request struct{ Messages []message }
		json.NewDecoder(r.Body).Decode(&request)
		for _, m := range request.Messages[len(request.Messages)-3:] {
			var result toolResult
			json.Unmarshal([]byte(m.Content), &result)
			if m.Role != "tool" || result.Error == "" || result.ExitCode != nil {
				t.Error("missing explicit no-execution result")
			}
		}
		reply(w, message{Role: "assistant", Content: "No changes."})
	}))
	defer server.Close()
	c := validConfig()
	c.BaseURL = server.URL
	client := newModelClient(c)
	defer client.close()
	a := newAgent(client, "test", t.TempDir(), io.Discard)
	a.approve = func(context.Context, string) (bool, error) { return false, nil }
	a.execute = func(context.Context, string) toolResult { t.Error("unapproved command started"); return toolResult{} }
	if err := a.turn(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
}

func TestInteractiveInputAndNew(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(404)
			return
		}
		var request struct{ Messages []message }
		json.NewDecoder(r.Body).Decode(&request)
		switch requests.Add(1) {
		case 1:
			reply(w, message{Role: "assistant", ToolCalls: []toolCall{call("denied", "[Console]::WriteLine('SHOULD_NOT_RUN')")}})
		case 2:
			if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "denied") {
				t.Error("blank approval did not deny")
			}
			reply(w, message{Role: "assistant", Content: "Denied."})
		case 3:
			if len(request.Messages) != 3 {
				t.Error("/new retained old messages")
			}
			reply(w, message{Role: "assistant", Content: "Fresh."})
		}
	}))
	defer server.Close()
	c := validConfig()
	c.BaseURL = server.URL
	path := filepath.Join(t.TempDir(), "portal.json")
	data, _ := json.Marshal(c)
	os.WriteFile(path, data, 0600)
	var out bytes.Buffer
	if err := runInput([]string{"--config", path}, strings.NewReader("inspect\n\n/new\nagain\n/exit\n"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 || strings.Contains(out.String(), "Stdout:\nSHOULD_NOT_RUN") {
		t.Fatal(out.String())
	}
}

func TestEOFAtApprovalStopsWithoutAnotherRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(404)
			return
		}
		requests.Add(1)
		reply(w, message{Role: "assistant", ToolCalls: []toolCall{call("eof", "throw 'MUST_NOT_RUN'")}})
	}))
	defer server.Close()
	c := validConfig()
	c.BaseURL = server.URL
	path := filepath.Join(t.TempDir(), "portal.json")
	data, _ := json.Marshal(c)
	os.WriteFile(path, data, 0600)
	var out bytes.Buffer
	if err := runInput([]string{"--config", path}, strings.NewReader("inspect\n"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || !strings.Contains(out.String(), "cancelled; outcome unknown") || !strings.Contains(out.String(), "Session summary") {
		t.Fatal(out.String())
	}
}

func TestInferenceFailuresAreBoundedAndSafe(t *testing.T) {
	for _, mode := range []string{"401", "403", "404", "429", "500", "redirect", "invalid", "empty", "thinking only", "incomplete", "oversized", "slow", "refusal"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "401":
					w.WriteHeader(401)
					fmt.Fprint(w, "SECRET")
				case "403", "404", "429", "500":
					status := map[string]int{"403": 403, "404": 404, "429": 429, "500": 500}[mode]
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(status)
				case "empty":
					reply(w, message{Role: "assistant"})
				case "thinking only":
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","reasoning_content":"thinking"},"finish_reason":"stop"}]}`)
				case "redirect":
					w.Header().Set("Location", "/leak")
					w.WriteHeader(302)
				case "invalid":
					fmt.Fprint(w, "SECRET")
				case "incomplete":
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[]},"finish_reason":"length"}]}`)
				case "oversized":
					fmt.Fprint(w, strings.Repeat("x", maxMessageBytes+1))
				case "slow":
					select {
					case <-r.Context().Done():
					case <-time.After(1500 * time.Millisecond):
					}
				case "refusal":
					reply(w, message{Role: "assistant", Refusal: "No."})
				}
			}))
			defer server.Close()
			c := validConfig()
			c.BaseURL = server.URL
			c.RequestTimeoutSeconds = 1
			client := newModelClient(c)
			defer client.close()
			_, err := client.complete(context.Background(), []message{{Role: "user", Content: "test"}}, true)
			if mode == "refusal" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestControlEscapesAndRetryDelay(t *testing.T) {
	if got := terminalText("a\x1b[2J\r\u202eb\n"); got != "a\\u001b[2J\\u000d\\u202eb\n" {
		t.Fatal(got)
	}
	if got := retryDelay("9223372036854775807", 0); got != 30*time.Second {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitRetry(ctx, time.Hour) != context.Canceled {
		t.Fatal("retry ignored cancellation")
	}
}

func TestTLSAndCancelledRequest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reply(w, message{Role: "assistant", Content: "ok"}) }))
	defer server.Close()
	server.Config.ErrorLog = nil
	c := validConfig()
	c.BaseURL = server.URL
	client := newModelClient(c)
	defer client.close()
	if _, err := client.complete(context.Background(), []message{{Role: "user", Content: "test"}}, true); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("untrusted certificate accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.complete(ctx, []message{{Role: "user", Content: "test"}}, true); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestInterruptReturnsFromTurnAndExitsAtIdle(t *testing.T) {
	c := newConsole(strings.NewReader(""))
	defer c.close()
	ctx, done := c.startTurn()
	c.signals <- os.Interrupt
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("interrupt did not cancel turn")
	}
	done()
	if c.ctx.Err() != nil {
		t.Fatal("turn interrupt ended session")
	}
	c.signals <- os.Interrupt
	select {
	case <-c.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle interrupt did not end session")
	}
}

func TestHistoryLimitDoesNotAccumulateRejectedPrompts(t *testing.T) {
	client := newModelClient(validConfig())
	defer client.close()
	a := newAgent(client, "test", t.TempDir(), io.Discard)
	a.history = append(a.history, message{Role: "user", Content: strings.Repeat("x", maxMessageBytes)})
	for i := 0; i < 10; i++ {
		if err := a.turn(context.Background(), "test"); err == nil || !strings.Contains(err.Error(), "/new") {
			t.Fatal(err)
		}
	}
	if len(a.history) != 3 {
		t.Fatal("rejected prompts increased memory use")
	}
}
