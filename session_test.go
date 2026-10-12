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

func TestStartupPromptCLI(t *testing.T) {
	for _, test := range []struct {
		name, want, input string
		args              []string
	}{
		{"quoted", "Check my free RAM", "next\n/exit\n", []string{"--prompt", "Check my free RAM"}},
		{"unquoted", "this is the prompt --literal text", "next\n/exit\n", []string{"--prompt", "this", "is", "the", "prompt", "--literal", "text"}},
		{"equals", "Check disk space", "next\n/exit\n", []string{"--prompt=Check disk space"}},
		{"unicode", "Inspect café\nThen report", "next\n/exit\n", []string{"--prompt", "Inspect café\nThen report"}},
		{"slash text", "/permissions automatic", "next\n/exit\n", []string{"--prompt", "/permissions automatic"}},
		{"EOF", "Start before EOF", "", []string{"--prompt", "Start before EOF"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				var request struct{ Messages []message }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				n := requests.Add(1)
				want := test.want
				if n == 2 {
					want = "next"
				}
				if len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].Content != want || n > 2 {
					t.Errorf("unexpected request %d: %#v", n, request.Messages)
				}
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Reply received."},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`)
			}))
			defer server.Close()
			cfg := validConfig()
			cfg.BaseURL = server.URL
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "portal.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"--config", path}, test.args...)
			var out bytes.Buffer
			if err := runInput(args, strings.NewReader(test.input), &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			turns := 2
			if test.input == "" {
				turns = 1
			}
			if int(requests.Load()) != turns || strings.Count(out.String(), "Session summary") != 1 || !strings.Contains(out.String(), fmt.Sprintf("Turns: %d; turn errors: 0", turns)) || !strings.Contains(out.String(), fmt.Sprintf("Tokens used (total): %d", turns*110)) || !strings.Contains(out.String(), "Tool permissions: ask") {
				t.Fatalf("bad startup prompt session: %s", out.String())
			}
			unchanged, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, unchanged) {
				t.Fatal("config changed")
			}
		})
	}
}

func TestStartupPromptUsesToolApproval(t *testing.T) {
	for _, answer := range []string{"y", "", "/exit"} {
		t.Run("answer="+answer, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					reply(w, message{Role: "assistant", ToolCalls: []toolCall{call("initial", "'marker'")}})
				} else {
					reply(w, message{Role: "assistant", Content: "Done."})
				}
			}))
			defer server.Close()
			cfg := validConfig()
			cfg.BaseURL = server.URL
			client := newModelClient(cfg)
			defer client.close()
			var out bytes.Buffer
			a := newAgent(client, "test", t.TempDir(), &out)
			executed := 0
			a.execute = func(context.Context, string) toolResult {
				executed++
				code := uint32(0)
				return toolResult{ExitCode: &code, Stdout: "marker"}
			}
			console := newConsole(strings.NewReader(answer + "\n/exit\n"))
			defer console.close()
			if err := interact(console, a, "Inspect first"); err != nil {
				t.Fatal(err)
			}
			want := 0
			if answer == "y" {
				want = 1
			}
			if executed != want || a.stats.turns != 1 || !strings.Contains(out.String(), "Run this command?") || strings.Count(out.String(), "Session summary") != 1 {
				t.Fatalf("startup prompt bypassed approval or consumed input: %s", out.String())
			}
		})
	}
}

func TestStartupPromptRejectsInvalidOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--prompt"}, {"--prompt", ""}, {"--prompt", "  "},
		{"--prompt", strings.Repeat("x", maxCommandBytes+1)},
		{"--check", "--prompt", "inspect"}, {"--tool-check", "--prompt", "inspect"},
		{"--startup-check", "--prompt", "inspect"}, {"--version", "--prompt", "inspect"},
		{"unexpected", "words"},
	} {
		var out bytes.Buffer
		if err := runInput(args, strings.NewReader(""), &out, io.Discard); err == nil || out.Len() != 0 {
			t.Fatal("invalid options started a session")
		}
	}
}

func TestSessionCommandsPermissionsGroupApprovalAndTotals(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		var request struct{ Messages []message }
		json.NewDecoder(r.Body).Decode(&request)
		m := message{Role: "assistant", Content: "Done."}
		switch n {
		case 1:
			m.ToolCalls = []toolCall{call("automatic", "'one'")}
		case 3:
			if len(request.Messages) != 3 {
				t.Error("/new did not clear history")
			}
			m.ToolCalls = []toolCall{call("group-one", "'two'"), call("group-two", "'three'")}
		case 4:
			m.ToolCalls = []toolCall{call("new-group", "'four'")}
		case 5:
			if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "denied") {
				t.Error("group approval leaked to the next response")
			}
		}
		reason := "stop"
		if len(m.ToolCalls) > 0 {
			reason = "tool_calls"
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": m, "finish_reason": reason}}, "usage": map[string]int32{"prompt_tokens": 100 * n, "completion_tokens": 10 * n}, "timings": map[string]int32{"predicted_n": 10 * n, "predicted_ms": 500 * n}})
	}))
	defer server.Close()
	c := validConfig()
	c.BaseURL, c.ApprovalMode = server.URL, "ask"
	client := newModelClient(c)
	defer client.close()
	var out bytes.Buffer
	a := newAgent(client, "test", t.TempDir(), &out)
	var executed []string
	a.execute = func(_ context.Context, command string) toolResult {
		executed = append(executed, command)
		code := uint32(0)
		return toolResult{ExitCode: &code, Stdout: "readable\r\noutput\r\n"}
	}
	console := newConsole(strings.NewReader("/help\n/status\n/permissions automatic\nfirst\n/permissions ask\n/new\n/permissions invalid\n/unknown\nsecond\n/status\n/new\na\n\n/status\n/exit\n"))
	defer console.close()
	if err := interact(console, a, ""); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 5 || strings.Join(executed, ",") != "'one','two','three'" || a.permissions != "ask" || client.config.ApprovalMode != "ask" {
		t.Fatalf("bad execution or permissions: %v; requests %d", executed, requests.Load())
	}
	if a.stats.turns != 2 || a.stats.executed != 3 || a.stats.denied != 1 || a.stats.tools != 4 || len(a.seen) != 4 || client.stats.promptTokens != 1500 || client.stats.outputTokens != 150 || client.stats.usageReplies != 5 {
		t.Fatalf("bad statistics: %#v; %#v", a.stats, client.stats)
	}
	for _, expected := range []string{"/help", "/permissions ask", "Unknown session command", "Use /new at the portal prompt", "readable\noutput", "Tokens sent (prompt): 1500", "Tokens received (output): 150", "Tokens used (total): 1650", "Generation rate: 20.0 tokens/s", "Tool permissions: ask"} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("missing %q in %s", expected, out.String())
		}
	}
	if strings.Count(out.String(), "Session summary") != 1 || strings.Contains(out.String(), `"stdout"`) {
		t.Fatal("summary repeated or JSON shown")
	}
}

func TestSessionSummaryOnIdleInterruptAndEOF(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		c := newConsole(strings.NewReader(""))
		if interrupt {
			c.signals <- os.Interrupt
			select {
			case <-c.ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("interrupt failed")
			}
		}
		client := newModelClient(validConfig())
		var out bytes.Buffer
		a := newAgent(client, "test", t.TempDir(), &out)
		err := interact(c, a, "")
		c.close()
		client.close()
		if err != nil || strings.Count(out.String(), "Session summary") != 1 || client.stats.requests != 0 {
			t.Fatalf("bad summary: %v; %s", err, out.String())
		}
	}
}

func TestCommandsAtToolApproval(t *testing.T) {
	for _, input := range []string{"/exit", "/permissions automatic"} {
		t.Run(input, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					reply(w, message{Role: "assistant", ToolCalls: []toolCall{call("pending", "'test'")}})
				} else {
					reply(w, message{Role: "assistant", Content: "Done."})
				}
			}))
			defer server.Close()
			cfg := validConfig()
			cfg.BaseURL, cfg.ApprovalMode = server.URL, "ask"
			client := newModelClient(cfg)
			defer client.close()
			var out bytes.Buffer
			a := newAgent(client, "test", t.TempDir(), &out)
			executed := 0
			a.execute = func(context.Context, string) toolResult {
				executed++
				code := uint32(0)
				return toolResult{ExitCode: &code}
			}
			console := newConsole(strings.NewReader("inspect\n" + input + "\n/exit\n"))
			defer console.close()
			if err := interact(console, a, ""); err != nil {
				t.Fatal(err)
			}
			if input == "/exit" && (executed != 0 || requests.Load() != 1) {
				t.Fatal("exit at approval continued execution")
			}
			if input == "/permissions automatic" && (executed != 1 || requests.Load() != 2 || a.permissions != "automatic") {
				t.Fatal("automatic mode did not apply to pending command")
			}
			if client.config.ApprovalMode != "ask" || strings.Count(out.String(), "Session summary") != 1 {
				t.Fatal("config changed or summary missing")
			}
		})
	}
}

func TestChecksRequireMarkersAndRejectExtraCommands(t *testing.T) {
	for _, mode := range []string{"text", "refusal", "wrong text", "tool", "extra tool", "wrong tool", "no tool"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					w.WriteHeader(404)
					return
				}
				n := requests.Add(1)
				switch mode {
				case "text":
					reply(w, message{Role: "assistant", Content: "PORTAL_OK"})
				case "refusal":
					reply(w, message{Role: "assistant", Refusal: "No."})
				case "wrong text":
					reply(w, message{Role: "assistant", Content: "Something else."})
				case "no tool":
					reply(w, message{Role: "assistant", Content: "PORTAL_TOOL_OK"})
				case "tool":
					if n == 1 {
						reply(w, message{Role: "assistant", ToolCalls: []toolCall{call("marker", toolCheckCommand)}})
					} else {
						reply(w, message{Role: "assistant", Content: "PORTAL_TOOL_OK"})
					}
				case "extra tool":
					reply(w, message{Role: "assistant", ToolCalls: []toolCall{call("marker", toolCheckCommand), call("forbidden", "throw 'MUST_NOT_RUN'")}})
				case "wrong tool":
					reply(w, message{Role: "assistant", ToolCalls: []toolCall{call("forbidden", "throw 'MUST_NOT_RUN'")}})
				}
			}))
			defer server.Close()
			c := validConfig()
			c.BaseURL, c.ApprovalMode = server.URL, "automatic"
			data, _ := json.Marshal(c)
			path := filepath.Join(t.TempDir(), "portal.json")
			os.WriteFile(path, data, 0600)
			option := "--tool-check"
			if mode == "text" || mode == "refusal" || mode == "wrong text" {
				option = "--check"
			}
			var out bytes.Buffer
			err := runInput([]string{"--config", path, option}, strings.NewReader(""), &out, io.Discard)
			wantSuccess := mode == "text" || mode == "tool"
			if (err == nil) != wantSuccess || (!wantSuccess && strings.Contains(out.String(), "check passed")) {
				t.Fatalf("unexpected check result: %v; %s", err, out.String())
			}
			if mode == "extra tool" && strings.Contains(out.String(), "Running command") {
				t.Fatal("extra batch executed a command")
			}
			unchanged, _ := os.ReadFile(path)
			if !bytes.Equal(data, unchanged) {
				t.Fatal("config rewritten")
			}
		})
	}
}

func TestReportedUsageDoesNotInventMissingTokens(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
		case 2:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":12}}`)
		case 3:
			reply(w, message{Role: "assistant", Content: "unknown usage"})
		case 4:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"partial usage"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0}}`)
		}
	}))
	defer server.Close()
	c := validConfig()
	c.BaseURL = server.URL
	client := newModelClient(c)
	defer client.close()
	for i := 0; i < 3; i++ {
		if _, err := client.complete(context.Background(), []message{{Role: "user", Content: "test"}}, false); err != nil {
			t.Fatal(err)
		}
	}
	if client.stats.requests != 3 || client.stats.attempts != 4 || client.stats.replies != 3 || client.stats.usageReplies != 1 || client.stats.promptTokens != 100 || client.stats.outputTokens != 12 || client.usageKnown {
		t.Fatalf("incorrect accounting: %#v", client.stats)
	}
}
