package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamCancellationIsCountedAndDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning":"working"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	c := validConfig()
	c.BaseURL = server.URL
	client := newModelClient(c)
	defer client.close()
	client.onThinking = func(string) { cancel() }
	_, err := client.complete(ctx, []message{{Role: "user", Content: "test"}}, true)
	if !errors.Is(err, context.Canceled) || client.stats.attempts != 1 || client.stats.cancelled != 1 || client.stats.failures != 1 || client.stats.usageReplies != 0 {
		t.Fatalf("bad cancellation: %v; %#v", err, client.stats)
	}
}

func TestStreamingThinkingRedactionToolAssemblyAndUsage(t *testing.T) {
	seen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream        bool
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		if !request.Stream || !request.StreamOptions.IncludeUsage {
			t.Error("stream or usage not requested")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"Evidence SEC\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-seen:
		case <-time.After(time.Second):
			t.Error("thinking was not delivered before completion")
			return
		}
		for _, data := range []string{
			`{"choices":[{"index":0,"delta":{"reasoning_content":"RET café 中文\u001b[2J","tool_calls":[{"index":0,"id":"stream-call","type":"function","function":{"name":"powershell_","arguments":"{\"command\":\""}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"call","arguments":"'marker'\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":123,"completion_tokens":25},"timings":{"predicted_n":25,"predicted_ms":500}}`,
			`[DONE]`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
	}))
	defer server.Close()
	c := validConfig()
	c.BaseURL, c.APIKey = server.URL, "SECRET"
	client := newModelClient(c)
	defer client.close()
	var out bytes.Buffer
	a := newAgent(client, "test", t.TempDir(), &out)
	a.colour = true
	thinking, finish := a.thinkingStream()
	client.onThinking = func(text string) {
		thinking(text)
		select {
		case seen <- struct{}{}:
		default:
		}
	}
	m, err := client.complete(context.Background(), a.history, true)
	finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ToolCalls) != 1 {
		t.Fatal("missing tool call")
	}
	command, err := toolCommand(m.ToolCalls[0])
	if err != nil || command != "'marker'" || m.ToolCalls[0].ID != "stream-call" || m.Thinking != "" {
		t.Fatalf("bad assembly: %#v; %v", m, err)
	}
	visible := strings.ReplaceAll(strings.ReplaceAll(out.String(), colourGrey, ""), colourReset, "")
	if strings.Contains(out.String(), "SECRET") || strings.Contains(out.String(), "\x1b[2J") || !strings.Contains(visible, "Evidence ****** café 中文\\u001b[2J") || strings.Count(out.String(), "\x1b[38;2;") != strings.Count(out.String(), colourReset) {
		t.Fatalf("unsafe streamed output: %q", out.String())
	}
	if client.stats.promptTokens != 123 || client.stats.outputTokens != 25 || client.usedTokens != 148 || client.stats.generationTime != 500*time.Millisecond {
		t.Fatalf("bad usage: %#v", client.stats)
	}
}

func TestIncompleteOrInvalidStreamsCannotRunTools(t *testing.T) {
	for _, mode := range []string{"missing done", "missing finish", "invalid JSON", "wrong index", "incomplete", "duplicate IDs", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"one\",\"type\":\"function\",\"function\":{\"name\":\"powershell_call\",\"arguments\":\"{\\\"command\\\":\\\"'MUST_NOT_RUN'\\\"}\"}}]}}]}\n\n")
				switch mode {
				case "missing finish":
					fmt.Fprint(w, "data: [DONE]\n\n")
					return
				case "invalid JSON":
					fmt.Fprint(w, "data: SECRET\n\n")
					return
				case "wrong index":
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":4}]}}]}\n\n")
					return
				case "incomplete":
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n")
					return
				case "duplicate IDs":
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"one\",\"type\":\"function\",\"function\":{\"name\":\"powershell_call\",\"arguments\":\"{\\\"command\\\":\\\"'MUST_NOT_RUN'\\\"}\"}}]}}]}\n\n")
				case "oversized":
					fmt.Fprint(w, "data: "+strings.Repeat("x", maxMessageBytes+1)+"\n\n")
					return
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
				if mode != "missing done" {
					fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}))
			defer server.Close()
			c := validConfig()
			c.BaseURL, c.ApprovalMode = server.URL, "automatic"
			client := newModelClient(c)
			defer client.close()
			var out bytes.Buffer
			a := newAgent(client, "test", t.TempDir(), &out)
			a.execute = func(context.Context, string) toolResult { t.Error("partial tool executed"); return toolResult{} }
			if err := a.turn(context.Background(), "test"); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("invalid stream accepted: %v", err)
			}
			if client.stats.attempts != 1 || a.stats.executed != 0 {
				t.Fatal("bad stream was retried or executed")
			}
		})
	}
}
