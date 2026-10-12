package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestProviderContextMetadata(t *testing.T) {
	for _, test := range []struct {
		name, body, model string
		want              int64
	}{
		{"provider", `{"data":[{"id":"other","context_length":1024},{"id":"test","context_length":262144,"context_window":262144,"meta":{"n_ctx":262144}}]}`, "test", 262144},
		{"active limit", `{"data":[{"id":"test","context_length":262144,"meta":{"n_ctx":32768}}]}`, "test", 32768},
		{"alias", `{"data":[{"id":"test","meta":{"n_ctx":8192,"llamaswap":{"aliases":["alias"]}}}]}`, "alias", 8192},
		{"max model len", `{"data":[{"id":"test","max_model_len":65536}]}`, "test", 65536},
		{"missing", `{"data":[{"id":"test"}]}`, "test", 0},
		{"wrong model", `{"data":[{"id":"other","context_length":262144}]}`, "test", 0},
		{"invalid", `{"data":[{"id":"test","context_length":"SECRET"}]}`, "test", 0},
		{"negative", `{"data":[{"id":"test","context_length":-1}]}`, "test", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer local" {
					t.Error("incorrect metadata request")
				}
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			c := validConfig()
			c.BaseURL = server.URL + "/v1"
			c.Model = test.model
			c.APIKey = "local"
			client := newModelClient(c)
			defer client.close()
			err := client.discoverContext(context.Background())
			if client.contextTokens != test.want || (err != nil) != (test.want == 0) || (err != nil && strings.Contains(err.Error(), "SECRET")) {
				t.Fatalf("context %d, error %v", client.contextTokens, err)
			}
			if client.requestByteLimit() < maxMessageBytes || client.requestByteLimit() > maxRequestBytes {
				t.Fatal("unbounded request budget")
			}
		})
	}
}

func TestToolBatchUsesActualOutputSize(t *testing.T) {
	for _, test := range []struct {
		name     string
		calls    int
		context  int64
		output   string
		truncate bool
	}{
		{"unknown context twenty calls", 20, 0, strings.Repeat("x", maxOutputBytes), false},
		{"provider context hundred calls", 100, 262144, strings.Repeat("x", maxOutputBytes), false},
		{"escaped output near byte limit", 20, 0, strings.Repeat("\x00中文", maxOutputBytes/7), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := make([]toolCall, test.calls)
			for i := range calls {
				calls[i] = call(fmt.Sprintf("call-%d", i), fmt.Sprintf("'inspection %d'", i))
			}
			var requests atomic.Int32
			var truncated atomic.Bool
			c := validConfig()
			c.ApprovalMode = "automatic"
			c.MaxToolCallsPerTurn = test.calls
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					reply(w, message{Role: "assistant", ToolCalls: calls})
					return
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				var request struct{ Messages []message }
				json.Unmarshal(body, &request)
				if len(request.Messages) != 4+test.calls {
					t.Errorf("missing results: %d messages", len(request.Messages))
					return
				}
				for i, m := range request.Messages[4:] {
					var result toolResult
					json.Unmarshal([]byte(m.Content), &result)
					if m.Role != "tool" || m.ToolCallID != calls[i].ID || result.ExitCode == nil || *result.ExitCode != 0 || result.Error != "" || !utf8.ValidString(result.Stdout) {
						t.Errorf("invalid result %d", i)
					}
					if result.Truncated {
						truncated.Store(true)
					}
					if !test.truncate && result.Stdout != test.output {
						t.Error("ordinary output was truncated")
					}
				}
				reply(w, message{Role: "assistant", Content: "Inspection complete."})
			}))
			defer server.Close()
			c.BaseURL = server.URL
			client := newModelClient(c)
			defer client.close()
			client.contextTokens = test.context
			a := newAgent(client, "test", t.TempDir(), io.Discard)
			executions := 0
			a.execute = func(_ context.Context, command string) toolResult {
				if command != fmt.Sprintf("'inspection %d'", executions) {
					t.Error("commands not serial")
				}
				executions++
				code := uint32(0)
				return toolResult{ExitCode: &code, Stdout: test.output}
			}
			if err := a.turn(context.Background(), "look around"); err != nil {
				t.Fatal(err)
			}
			if executions != test.calls || truncated.Load() != test.truncate {
				t.Fatalf("executions %d, truncated %v", executions, truncated.Load())
			}
			if _, err := client.requestBody(a.history, true); err != nil {
				t.Fatal("history no longer fits:", err)
			}
		})
	}
}

func TestServerTokenUsageAndLargeConversation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":40000,"completion_tokens":123}}`)
	}))
	defer server.Close()
	c := validConfig()
	c.BaseURL = server.URL
	client := newModelClient(c)
	defer client.close()
	client.contextTokens = 262144
	history := []message{{Role: "user", Content: strings.Repeat("x", maxMessageBytes+1)}}
	if _, err := client.complete(context.Background(), history, true); err != nil {
		t.Fatal(err)
	}
	if client.usedTokens != 40123 {
		t.Fatal("provider token count not used")
	}
	client.contextTokens = 0
	if _, err := client.requestBody(history, true); err == nil {
		t.Fatal("unknown-provider memory limit not enforced")
	}
}
