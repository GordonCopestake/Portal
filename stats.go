package main

import (
	"fmt"
	"time"
)

type modelStats struct {
	requests, attempts, replies, failures, cancelled, usageReplies int
	promptTokens, outputTokens, generationTokens                   int64
	requestTime, replyTime, generationTime                         time.Duration
}

type sessionStats struct {
	started                                                         time.Time
	turns, turnErrors, tools, executed, denied, invalid, toolErrors int
	timedOut, cancelled, truncated                                  int
	toolTime                                                        time.Duration
}

type tokenUsage struct {
	Prompt     *int64 `json:"prompt_tokens"`
	Completion *int64 `json:"completion_tokens"`
}

type modelTiming struct {
	PredictedN  int64   `json:"predicted_n"`
	PredictedMS float64 `json:"predicted_ms"`
}

func (c *modelClient) recordUsage(usage *tokenUsage, timing modelTiming, elapsed time.Duration) {
	c.stats.replies++
	c.usedTokens = 0
	c.usageKnown = false
	if usage == nil || usage.Prompt == nil || usage.Completion == nil || *usage.Prompt < 0 || *usage.Completion < 0 || *usage.Prompt > 1<<40 || *usage.Completion > 1<<40 {
		return
	}
	c.usedTokens = *usage.Prompt + *usage.Completion
	c.usageKnown = true
	c.stats.usageReplies++
	c.stats.promptTokens += *usage.Prompt
	c.stats.outputTokens += *usage.Completion
	c.stats.replyTime += elapsed
	if timing.PredictedN == *usage.Completion && timing.PredictedMS > 0 && timing.PredictedMS <= 3600000 {
		c.stats.generationTokens += timing.PredictedN
		c.stats.generationTime += time.Duration(timing.PredictedMS * float64(time.Millisecond))
	}
}

func (a *agent) status(title string) {
	s, m := a.stats, a.client.stats
	a.write(colourWhite, fmt.Sprintf("\n%s\nModel: %s\nTool permissions: %s\nSession duration: %s\nTurns: %d; turn errors: %d\nModel requests: %d; HTTP attempts: %d; failed: %d; cancelled: %d\nModel request time: %.2fs\n",
		title, a.client.config.Model, a.permissions, time.Since(s.started).Round(time.Second), s.turns, s.turnErrors, m.requests, m.attempts, m.failures, m.cancelled, m.requestTime.Seconds()))
	if a.client.contextTokens > 0 {
		a.write(colourWhite, fmt.Sprintf("Context limit: %d tokens\n", a.client.contextTokens))
		if a.client.usageKnown {
			a.write(colourWhite, fmt.Sprintf("Last reply token use: %d tokens\n", a.client.usedTokens))
		} else {
			a.write(colourWhite, "Last reply token use: not reported\n")
		}
	}
	if m.usageReplies == 0 {
		a.write(colourWhite, "Tokens: not reported yet\n")
	} else {
		a.write(colourWhite, fmt.Sprintf("Tokens sent (prompt): %d\nTokens received (output): %d\nTokens used (total): %d\nUsage reported: %d / %d replies\n", m.promptTokens, m.outputTokens, m.promptTokens+m.outputTokens, m.usageReplies, m.replies))
		if m.replyTime > 0 {
			a.write(colourWhite, fmt.Sprintf("Reply rate: %.1f output tokens/s (includes network and prompt processing)\n", float64(m.outputTokens)/m.replyTime.Seconds()))
		}
		if m.generationTime > 0 {
			a.write(colourWhite, fmt.Sprintf("Generation rate: %.1f tokens/s (provider timing; %d output tokens)\n", float64(m.generationTokens)/m.generationTime.Seconds(), m.generationTokens))
		}
		a.write(colourWhite, "Prompt counts include history sent again. Output counts include provider-counted thinking and tool calls.\n")
	}
	if m.usageReplies < m.replies || m.failures > 0 || m.attempts > m.requests {
		a.write(colourWhite, "Token totals cover reported usage only. Failed or interrupted requests can have unreported usage.\n")
	}
	a.write(colourWhite, fmt.Sprintf("Tools requested: %d; run attempts: %d; denied: %d; invalid: %d\nTool errors: %d; timed out: %d; cancelled: %d; truncated: %d\nTool run time: %.2fs\nConversation messages: %d; retained call IDs: %d\n", s.tools, s.executed, s.denied, s.invalid, s.toolErrors, s.timedOut, s.cancelled, s.truncated, s.toolTime.Seconds(), len(a.history), len(a.seen)))
}
