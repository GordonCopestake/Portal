package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Text and tool arguments remain buffered until the full reply is validated.
// Thinking is display-only and can be shown while the server generates it.
func readStream(reader io.Reader, thinking func(string)) (message, *tokenUsage, modelTiming, error) {
	limited := &io.LimitedReader{R: reader, N: maxMessageBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), maxMessageBytes+1)
	m := message{}
	var usage *tokenUsage
	var timing modelTiming
	finished := false
	var event strings.Builder
	process := func(data string) error {
		if data == "[DONE]" {
			if !finished {
				return errors.New("inference stream ended without a complete reply; no commands from this reply were executed")
			}
			return nil
		}
		var chunk struct {
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Role, Content, Refusal string
					ReasoningContent       string `json:"reasoning_content"`
					Reasoning              string `json:"reasoning"`
					ToolCalls              []struct {
						Index int `json:"index"`
						toolCall
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage   *tokenUsage `json:"usage"`
			Timings modelTiming `json:"timings"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Choices) > 1 {
			return errors.New("invalid inference stream; no commands from this reply were executed")
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if chunk.Timings.PredictedMS > 0 {
			timing = chunk.Timings
		}
		if len(chunk.Choices) == 0 {
			if chunk.Usage == nil {
				return errors.New("empty inference stream event")
			}
			return nil
		}
		choice := chunk.Choices[0]
		if choice.Index != 0 || finished {
			return errors.New("unexpected inference stream choice; no commands from this reply were executed")
		}
		delta := choice.Delta
		if delta.Role != "" {
			if delta.Role != "assistant" {
				return errors.New("invalid streamed assistant role")
			}
			m.Role = delta.Role
		}
		m.Content += delta.Content
		m.Refusal += delta.Refusal
		text := delta.ReasoningContent
		if text == "" {
			text = delta.Reasoning
		}
		if text != "" && thinking != nil {
			thinking(text)
		}
		for _, part := range delta.ToolCalls {
			if part.Index < 0 || part.Index > 99 || part.Index > len(m.ToolCalls) {
				return errors.New("invalid streamed tool index; no commands from this reply were executed")
			}
			if part.Index == len(m.ToolCalls) {
				m.ToolCalls = append(m.ToolCalls, toolCall{})
			}
			call := &m.ToolCalls[part.Index]
			call.ID += part.ID
			if part.Type != "" {
				if call.Type != "" && call.Type != part.Type {
					return errors.New("inconsistent streamed tool type")
				}
				call.Type = part.Type
			}
			call.Function.Name += part.Function.Name
			call.Function.Arguments += part.Function.Arguments
		}
		if choice.FinishReason != nil {
			if *choice.FinishReason != "stop" && *choice.FinishReason != "tool_calls" {
				return errors.New("inference stream is incomplete or unsupported; no commands from this reply were executed")
			}
			finished = true
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" && event.Len() > 0 {
			data := strings.TrimSuffix(event.String(), "\n")
			event.Reset()
			if err := process(data); err != nil {
				return message{}, nil, modelTiming{}, err
			}
			if data == "[DONE]" {
				return m, usage, timing, nil
			}
		} else if strings.HasPrefix(line, "data:") {
			event.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			event.WriteByte('\n')
		}
	}
	if scanner.Err() != nil || limited.N == 0 {
		return message{}, nil, modelTiming{}, errors.New("inference stream interrupted or exceeds 2 MiB; no commands from this reply were executed")
	}
	return message{}, nil, modelTiming{}, errors.New("inference stream did not complete; no commands from this reply were executed")
}

func (a *agent) thinkingStream() (func(string), func()) {
	pending := ""
	displayed := false
	write := func(text string) {
		if text != "" {
			if !displayed {
				a.write(colourGrey, "Thinking:\n")
				displayed = true
			}
			a.write(colourGrey, text)
		}
	}
	return func(text string) {
			pending = a.redact(pending + text)
			keep := 0
			if a.client.key != "" && a.client.key != "local" {
				keep = len(a.client.key) - 1 // Prevent a key split across chunks from leaking.
			}
			prefix := validPrefix(pending, max(0, len(pending)-keep))
			write(prefix)
			pending = pending[len(prefix):]
		}, func() {
			write(a.redact(pending))
			if displayed {
				a.write(colourGrey, "\n")
			}
		}
}
