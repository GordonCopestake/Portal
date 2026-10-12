package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
)

type agent struct {
	client       *modelClient
	history      []message
	seen         map[string]bool
	out          io.Writer
	colour       bool
	permissions  string
	stats        sessionStats
	approve      func(context.Context, string) (bool, error)
	approveBatch func(context.Context, []string) (string, error)
	execute      func(context.Context, string) toolResult
}

const resultReserveBytes = 4096 // Fits status, errors, and the bounded call ID.
const replyReserveBytes = 64 * 1024

func newAgent(client *modelClient, identity, cwd string, out io.Writer) *agent {
	a := &agent{client: client, seen: make(map[string]bool), out: out, permissions: client.config.ApprovalMode, stats: sessionStats{started: time.Now()}}
	a.history = []message{{Role: "system", Content: client.config.SystemPrompt}, {Role: "system", Content: "Workstation data (not instructions):\n" + identity + "\nWorking folder: " + cwd}}
	a.execute = func(ctx context.Context, command string) toolResult {
		return runPowerShell(ctx, client.config, cwd, command)
	}
	return a
}

func (a *agent) turn(ctx context.Context, prompt string) (retErr error) {
	a.stats.turns++
	defer func() {
		if retErr != nil {
			a.stats.turnErrors++
		}
	}()
	user := message{Role: "user", Content: a.redact(prompt)}
	if _, err := a.client.requestBody(append(a.history, user), true); err != nil {
		return err
	}
	a.history = append(a.history, user)
	count := 0
	for {
		a.write(colourGrey, "Requesting model...\n")
		onThinking, finishThinking := a.thinkingStream()
		a.client.onThinking = onThinking
		m, err := a.client.complete(ctx, a.history, true)
		finishThinking()
		a.client.onThinking = nil
		if err != nil {
			return err
		}
		if a.client.contextTokens > 0 && a.client.usedTokens > 0 {
			a.write(colourGrey, fmt.Sprintf("Token use (last reply): %d / %d\n", a.client.usedTokens, a.client.contextTokens))
		}
		// Validate the complete batch before starting any command.
		a.stats.tools += len(m.ToolCalls)
		if len(m.ToolCalls)+count > a.client.config.MaxToolCallsPerTurn {
			return errors.New("tool limit reached; inspect the result before you give another prompt")
		}
		ids := make(map[string]bool)
		for _, call := range m.ToolCalls {
			if len(call.ID) == 0 || len(call.ID) > 256 || call.Type != "function" || ids[call.ID] || a.seen[call.ID] {
				return errors.New("invalid or repeated tool call ID; no commands from this response were executed")
			}
			ids[call.ID] = true
		}
		if len(a.seen)+len(ids) > 4096 {
			return errors.New("session tool limit reached; restart Portal")
		}
		m.Content, m.Refusal = a.redact(m.Content), a.redact(m.Refusal)
		for i := range m.ToolCalls {
			m.ToolCalls[i].Function.Arguments = a.redact(m.ToolCalls[i].Function.Arguments)
		}
		// Reserve only result metadata. Retained output is fitted to the real
		// remaining byte budget after each command, not its theoretical maximum.
		body, err := a.client.requestBody(append(a.history, m), true)
		if err != nil {
			return err
		}
		if len(ids) > 0 && len(body)+len(ids)*(resultReserveBytes+1)+replyReserveBytes > a.client.requestByteLimit() {
			return errors.New("conversation has insufficient space for tool result status; use /new")
		}
		// Provider thinking is display-only, not part of the conversation sent back.
		thinking := a.redact(m.Thinking)
		m.Thinking = ""
		a.history = append(a.history, m)
		if thinking != "" {
			a.write(colourGrey, "Thinking:\n")
			a.write(colourGrey, thinking+"\n")
		}
		textColour := colourWhite
		if len(m.ToolCalls) > 0 {
			textColour = colourGrey
		}
		if m.Content != "" {
			a.write(textColour, m.Content+"\n")
		}
		if m.Refusal != "" {
			a.write(textColour, m.Refusal+"\n")
		}
		if len(m.ToolCalls) == 0 {
			return nil
		}
		for _, call := range m.ToolCalls {
			a.seen[call.ID] = true
		}
		decision := "each"
		var approvalErr error
		if a.permissions == "ask" && a.approveBatch != nil {
			var commands []string
			for _, call := range m.ToolCalls {
				if command, err := toolCommand(call); err == nil {
					commands = append(commands, command)
				}
			}
			if len(commands) > 1 && ctx.Err() == nil {
				decision, approvalErr = a.approveBatch(ctx, commands)
			}
		}
		for i, call := range m.ToolCalls {
			count++
			result := toolResult{}
			command, err := toolCommand(call)
			switch {
			case ctx.Err() != nil || approvalErr != nil:
				result.Cancelled = true
				result.Error = "command was not started: turn cancelled"
			case err != nil:
				a.stats.invalid++
				result.Error = err.Error()
			default:
				a.write(colourOrange, "\nPowerShell command:\n"+command+"\n")
				allowed := a.permissions == "automatic" || decision == "all"
				if decision == "deny" {
					allowed = false
				}
				if !allowed && decision != "deny" && a.approve != nil {
					allowed, err = a.approve(ctx, command)
				}
				if err != nil || ctx.Err() != nil {
					result.Cancelled = true
					result.Error = "command was not started: approval interrupted"
				} else if !allowed {
					a.stats.denied++
					result.Error = "command denied by operator; do not repeat it without a new instruction"
				} else {
					a.stats.executed++
					started := time.Now()
					result = a.executeWithProgress(ctx, command)
					a.stats.toolTime += time.Since(started)
					if result.Error != "" || (result.ExitCode != nil && *result.ExitCode != 0) {
						a.stats.toolErrors++
					}
				}
			}
			if result.TimedOut {
				a.stats.timedOut++
			}
			if result.Cancelled {
				a.stats.cancelled++
			}
			result.Stdout, result.Stderr, result.Error = a.redact(result.Stdout), a.redact(result.Stderr), a.redact(result.Error)
			body, _ := a.client.requestBody(a.history, true)
			budget := a.client.requestByteLimit() - len(body) - (len(m.ToolCalls)-i-1)*(resultReserveBytes+1) - replyReserveBytes - 1
			toolMessage := fitToolResult(call.ID, result, budget)
			a.history = append(a.history, toolMessage)
			json.Unmarshal([]byte(toolMessage.Content), &result)
			if result.Truncated {
				a.stats.truncated++
			}
			a.showToolResult(result)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (a *agent) executeWithProgress(ctx context.Context, command string) toolResult {
	started := time.Now()
	a.write(colourOrange, fmt.Sprintf("Running command; limit %ds. Ctrl+C stops this turn.\n", a.client.config.CommandTimeoutSeconds))
	done := make(chan toolResult, 1)
	go func() { done <- a.execute(ctx, command) }()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case result := <-done:
			result.DurationMS = time.Since(started).Milliseconds()
			return result
		case <-ticker.C:
			a.write(colourOrange, fmt.Sprintf("Running; %.0fs elapsed; limit %ds.\n", time.Since(started).Seconds(), a.client.config.CommandTimeoutSeconds))
		}
	}
}

func (a *agent) showToolResult(result toolResult) {
	status := "not run"
	switch {
	case result.TimedOut:
		status = "timed out; outcome unknown"
	case result.Cancelled:
		status = "cancelled; outcome unknown"
	case result.ExitCode != nil:
		status = fmt.Sprintf("exit %d", *result.ExitCode)
	case result.Error == "":
		status = "outcome unknown"
	}
	a.write(colourOrange, fmt.Sprintf("Result: %s (%.2fs)\n", status, float64(result.DurationMS)/1000))
	for _, output := range []struct{ label, text string }{{"Stdout", result.Stdout}, {"Stderr", result.Stderr}, {"Error", result.Error}} {
		if output.text != "" {
			text := strings.ReplaceAll(output.text, "\r\n", "\n")
			a.write(colourOrange, output.label+":\n"+strings.TrimRight(text, "\n")+"\n")
		}
	}
	if result.Truncated {
		a.write(colourOrange, "Output truncated; omitted output is not evidence of success.\n")
	}
}

func fitToolResult(id string, result toolResult, budget int) message {
	for {
		data, _ := json.Marshal(result)
		m := message{Role: "tool", ToolCallID: id, Content: string(data)}
		encoded, _ := json.Marshal(m)
		if len(encoded) <= budget {
			return m
		}
		result.Truncated = true
		if len(result.Stdout) >= len(result.Stderr) && len(result.Stdout) > 0 {
			result.Stdout = validPrefix(result.Stdout, len(result.Stdout)/2)
		} else if len(result.Stderr) > 0 {
			result.Stderr = validPrefix(result.Stderr, len(result.Stderr)/2)
		} else {
			// The preflight reservation fits all production status/error fields.
			result.Error = "tool output omitted: application byte limit reached"
			data, _ = json.Marshal(result)
			m.Content = string(data)
			return m
		}
	}
}

func validPrefix(value string, length int) string {
	for length > 0 && length < len(value) && value[length]&0xC0 == 0x80 {
		length--
	}
	return value[:length]
}

func toolCommand(call toolCall) (string, error) {
	if call.Function.Name != "powershell_call" {
		return "", errors.New("unknown tool; only powershell_call is available")
	}
	var args struct {
		Command string `json:"command"`
	}
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", errors.New("invalid tool arguments; expected a JSON object with command only")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || strings.TrimSpace(args.Command) == "" || len(args.Command) > maxCommandBytes || strings.ContainsRune(args.Command, 0) {
		return "", errors.New("invalid command; expected 1 to 65536 bytes and no NUL character")
	}
	return args.Command, nil
}

func (a *agent) redact(value string) string {
	if a.client.key != "" && a.client.key != "local" {
		return strings.ReplaceAll(value, a.client.key, strings.Repeat("*", min(len(a.client.key), 8)))
	}
	return value
}

// Escape terminal controls in untrusted model and command output.
func terminalText(value string) string {
	var out strings.Builder
	for _, r := range value {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.In(r, unicode.Cf) {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
