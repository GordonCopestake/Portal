package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxMessageBytes = 2 * 1024 * 1024  // Response cap and default request cap.
const maxRequestBytes = 32 * 1024 * 1024 // Application memory ceiling, not a token count.

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Refusal    string     `json:"refusal,omitempty"`
	Thinking   string     `json:"-"` // Optional provider output, never sent in requests.
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

var powershellSchema = json.RawMessage(`{"type":"function","function":{"name":"powershell_call","description":"Run a PowerShell command on the local Windows workstation. Each call starts a new process in the reported working folder. State does not persist. Return code, stdout, stderr, and cancellation state are returned. Do not use this tool to launch detached background work.","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}}}`)

type modelClient struct {
	config        config
	key           string
	http          *http.Client
	contextTokens int64
	usedTokens    int64
	usageKnown    bool
	stats         modelStats
	onThinking    func(string)
}

func newModelClient(c config) *modelClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &modelClient{config: c, key: c.resolvedKey(), http: &http.Client{
		Transport: transport,
		// A redirect can disclose the prompt as well as credentials.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *modelClient) close() { c.http.CloseIdleConnections() }

func (c *modelClient) requestByteLimit() int {
	// Allow room for UTF-8, escaped JSON, tool schemas, and results. The
	// provider counts model tokens; this is only a bounded memory budget.
	return max(maxMessageBytes, int(min(c.contextTokens, maxRequestBytes/64))*64)
}

func (c *modelClient) requestBody(history []message, tools bool) ([]byte, error) {
	reqData := struct {
		Model         string            `json:"model"`
		Messages      []message         `json:"messages"`
		Tools         []json.RawMessage `json:"tools,omitempty"`
		Stream        bool              `json:"stream"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}{Model: c.config.Model, Messages: history, Stream: true}
	reqData.StreamOptions.IncludeUsage = true
	if tools {
		reqData.Tools = []json.RawMessage{powershellSchema}
	}
	body, err := json.Marshal(reqData)
	if err != nil {
		return nil, errors.New("cannot encode conversation")
	}
	if len(body) > c.requestByteLimit() {
		return nil, fmt.Errorf("conversation exceeds the application byte limit (%d MiB); use /new", c.requestByteLimit()/(1024*1024))
	}
	return body, nil
}

func (c *modelClient) complete(ctx context.Context, history []message, tools bool) (_ message, retErr error) {
	body, err := c.requestBody(history, tools)
	if err != nil {
		return message{}, err
	}
	started := time.Now()
	c.stats.requests++
	c.usedTokens = 0
	c.usageKnown = false
	defer func() {
		c.stats.requestTime += time.Since(started)
		if retErr != nil {
			c.stats.failures++
			if errors.Is(retErr, context.Canceled) {
				c.stats.cancelled++
			}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.config.RequestTimeoutSeconds)*time.Second)
	defer cancel()
	endpoint := strings.TrimRight(c.config.BaseURL, "/") + "/chat/completions"
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return message{}, errors.New("cannot create inference request; check base_url")
		}
		req.Header.Set("Content-Type", "application/json")
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		c.stats.attempts++
		attemptStarted := time.Now()
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return message{}, ctx.Err()
			}
			var certErr *http.ProtocolError
			var unknown x509.UnknownAuthorityError
			var invalid x509.CertificateInvalidError
			var hostname x509.HostnameError
			if errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname) || errors.As(err, &certErr) {
				return message{}, errors.New("TLS or protocol check failed; check the endpoint and certificate")
			}
			if attempt < 2 {
				if err := waitRetry(ctx, time.Duration(attempt+1)*time.Second); err != nil {
					return message{}, err
				}
				continue
			}
			return message{}, errors.New("inference connection failed; check the server, DNS, and network")
		}
		if resp.StatusCode == http.StatusOK && strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			m, usage, timing, readErr := readStream(resp.Body, c.onThinking)
			resp.Body.Close()
			if readErr != nil {
				if ctx.Err() != nil {
					return message{}, ctx.Err()
				}
				return message{}, readErr // Do not replay an interrupted stream.
			}
			c.recordUsage(usage, timing, time.Since(attemptStarted))
			if err := validateReply(m, tools); err != nil {
				return message{}, err
			}
			return m, nil
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes+1))
		resp.Body.Close()
		if readErr != nil {
			if ctx.Err() != nil {
				return message{}, ctx.Err()
			}
			if attempt < 2 {
				if err := waitRetry(ctx, time.Second); err != nil {
					return message{}, err
				}
				continue
			}
			return message{}, errors.New("inference response was interrupted; try the request again")
		}
		if len(data) > maxMessageBytes {
			return message{}, errors.New("inference response exceeds 2 MiB")
		}
		if resp.StatusCode != http.StatusOK {
			retryable := resp.StatusCode == 429 || resp.StatusCode == 500 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
			if retryable && attempt < 2 {
				delay := retryDelay(resp.Header.Get("Retry-After"), attempt)
				if err := waitRetry(ctx, delay); err != nil {
					return message{}, err
				}
				continue
			}
			return message{}, fmt.Errorf("inference HTTP %d; %s", resp.StatusCode, httpRecovery(resp.StatusCode))
		}
		var response struct {
			Choices []struct {
				Message struct {
					message
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage   *tokenUsage `json:"usage"`
			Timings modelTiming `json:"timings"`
		}
		if err := json.Unmarshal(data, &response); err != nil || len(response.Choices) != 1 {
			return message{}, errors.New("invalid inference response; expected one complete assistant choice")
		}
		choice := response.Choices[0]
		c.recordUsage(response.Usage, response.Timings, time.Since(attemptStarted))
		m := choice.Message.message
		m.Thinking = choice.Message.ReasoningContent
		if m.Thinking == "" {
			m.Thinking = choice.Message.Reasoning
		}
		if m.Role != "assistant" || (choice.FinishReason != "stop" && choice.FinishReason != "tool_calls") {
			return message{}, errors.New("inference response is incomplete or unsupported; no commands from this reply were executed")
		}
		if err := validateReply(m, tools); err != nil {
			return message{}, err
		}
		return m, nil
	}
	return message{}, errors.New("inference retry limit reached")
}

func validateReply(m message, tools bool) error {
	if m.Role != "assistant" {
		return errors.New("invalid assistant role; no commands from this reply were executed")
	}
	if len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) == "" && m.Refusal == "" {
		return errors.New("inference returned an empty answer")
	}
	if !tools && len(m.ToolCalls) > 0 {
		return errors.New("server requested tools when tools were disabled")
	}
	return nil
}

// Context metadata is optional in the OpenAI-compatible model list. Missing
// metadata must not block a working completion endpoint or guess a token limit.
func (c *modelClient) discoverContext(ctx context.Context) error {
	c.contextTokens = 0
	ctx, cancel := context.WithTimeout(ctx, min(3*time.Second, time.Duration(c.config.RequestTimeoutSeconds)*time.Second))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.config.BaseURL, "/")+"/models", nil)
	if err != nil {
		return errors.New("cannot create model metadata request")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("model metadata request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("model metadata HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes+1))
	if err != nil || len(data) > maxMessageBytes {
		return errors.New("model metadata response is incomplete or too large")
	}
	var models struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int64  `json:"context_length"`
			ContextWindow int64  `json:"context_window"`
			MaxModelLen   int64  `json:"max_model_len"`
			Meta          struct {
				NCtx      int64 `json:"n_ctx"`
				LlamaSwap struct {
					Aliases []string `json:"aliases"`
				} `json:"llamaswap"`
			} `json:"meta"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &models); err != nil {
		return errors.New("invalid model metadata JSON")
	}
	for _, model := range models.Data {
		match := model.ID == c.config.Model
		for _, alias := range model.Meta.LlamaSwap.Aliases {
			match = match || alias == c.config.Model
		}
		if !match {
			continue
		}
		for _, limit := range []int64{model.ContextLength, model.ContextWindow, model.MaxModelLen, model.Meta.NCtx} {
			if limit > 0 && (c.contextTokens == 0 || limit < c.contextTokens) {
				c.contextTokens = limit
			}
		}
		if c.contextTokens == 0 {
			return errors.New("selected model has no context metadata")
		}
		return nil
	}
	return errors.New("selected model is absent from model metadata")
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryDelay(value string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, 30)) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(0, min(time.Until(date), 30*time.Second))
	}
	return time.Duration(attempt+1) * time.Second
}

func httpRecovery(status int) string {
	switch status {
	case 401, 403:
		return "check the configured credential and server access"
	case 404:
		return "check the API base URL and model"
	case 400, 413, 422:
		return "check model tool support or use /new to reduce the conversation"
	case 429:
		return "server limit reached; try again later"
	default:
		return "check the inference server; no browser login or update is required"
	}
}
