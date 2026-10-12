package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
)

type inputLine struct {
	text string
	err  error
}

type console struct {
	ctx     context.Context
	stop    context.CancelFunc
	lines   chan inputLine
	mu      sync.Mutex
	active  context.CancelFunc
	signals chan os.Signal
}

func newConsole(input io.Reader) *console {
	ctx, stop := context.WithCancel(context.Background())
	c := &console{ctx: ctx, stop: stop, lines: make(chan inputLine), signals: make(chan os.Signal, 1)}
	signal.Notify(c.signals, os.Interrupt)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.signals:
				c.mu.Lock()
				cancel := c.active
				if cancel == nil {
					cancel = c.stop
				}
				cancel()
				c.mu.Unlock()
			}
		}
	}()
	go func() {
		defer close(c.lines)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), maxCommandBytes+1)
		for scanner.Scan() {
			select {
			case c.lines <- inputLine{text: scanner.Text()}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case c.lines <- inputLine{err: errors.New("console input failed or exceeds 64 KiB")}:
			case <-ctx.Done():
			}
		}
	}()
	return c
}

func (c *console) close() { signal.Stop(c.signals); c.stop() }

func (c *console) read(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-c.ctx.Done():
		return "", c.ctx.Err()
	case line, ok := <-c.lines:
		if !ok {
			return "", io.EOF
		}
		return line.text, line.err
	}
}

func (c *console) startTurn() (context.Context, func()) {
	ctx, cancel := context.WithCancel(c.ctx)
	c.mu.Lock()
	c.active = cancel
	c.mu.Unlock()
	return ctx, func() { c.mu.Lock(); c.active = nil; cancel(); c.mu.Unlock() }
}

func interact(c *console, a *agent, initialPrompt string) error {
	defer a.status("Session summary")
	fmt.Fprintln(a.out, "Enter a prompt. /help lists session commands. Ctrl+C stops a turn; at the prompt, it exits.")
	readApproval := func(ctx context.Context, prompt, automaticAnswer string) (string, error) {
		for {
			a.write(colourOrange, prompt)
			line, err := c.read(ctx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					c.stop()
				}
				return "", err
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "/") {
				_, exit := a.sessionCommand(line, false)
				if exit {
					c.stop()
					return "", context.Canceled
				}
				if a.permissions == "automatic" {
					return automaticAnswer, nil
				}
				continue
			}
			return strings.ToLower(line), nil
		}
	}
	a.approve = func(ctx context.Context, _ string) (bool, error) {
		line, err := readApproval(ctx, "Run this command? [y/N] ", "y")
		return line == "y" || line == "yes", err
	}
	a.approveBatch = func(ctx context.Context, commands []string) (string, error) {
		a.write(colourOrange, "\nCommand group (approval applies only to these exact commands):\n")
		for i, command := range commands {
			a.write(colourOrange, fmt.Sprintf("\n%d. %s\n", i+1, command))
		}
		line, err := readApproval(ctx, "Approve [a] all, [i] individually, [n] none? [n] ", "all")
		switch line {
		case "a", "all":
			return "all", err
		case "i", "each":
			return "each", err
		default:
			return "deny", err
		}
	}
	for {
		fmt.Fprint(a.out, "\nportal> ")
		line := initialPrompt
		fromArgs := line != ""
		initialPrompt = ""
		var err error
		if fromArgs {
			a.write(colourWhite, a.redact(line)+"\n")
		} else {
			line, err = c.read(c.ctx)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !fromArgs {
			if handled, exit := a.sessionCommand(line, true); handled {
				if exit {
					return nil
				}
				continue
			}
		}
		ctx, done := c.startTurn()
		err = a.turn(ctx, line)
		done()
		if err != nil {
			fmt.Fprintln(a.out, "Portal:", terminalText(explainError(err)))
		}
	}
}

func (a *agent) sessionCommand(line string, allowNew bool) (handled, exit bool) {
	if !strings.HasPrefix(line, "/") {
		return false, false
	}
	args := strings.Fields(line)
	if len(args) == 0 {
		return false, false
	}
	if args[0] != "/permissions" && len(args) != 1 {
		a.write(colourWhite, "This command takes no arguments. Use /help.\n")
		return true, false
	}
	switch args[0] {
	case "/help":
		a.write(colourWhite, "Session commands:\n/help                     Show this list\n/status                   Show session statistics\n/permissions              Show the tool permission mode\n/permissions ask          Ask before tool execution\n/permissions automatic    Run tools without approval\n/new                      Clear conversation; keep session statistics and permissions\n/exit                     End the session and show its summary\n\nCtrl+C stops the active turn. At the prompt, it ends the session.\nAt tool approval, /help, /status, /permissions, and /exit are also available.\n")
	case "/status":
		a.status("Session status")
	case "/permissions":
		if len(args) == 1 {
			a.write(colourWhite, "Tool permissions: "+a.permissions+". Use /permissions ask or /permissions automatic.\n")
			break
		}
		if len(args) != 2 || (args[1] != "ask" && args[1] != "automatic") {
			a.write(colourWhite, "Use /permissions ask or /permissions automatic.\n")
			break
		}
		a.permissions = args[1]
		a.write(colourWhite, "Tool permissions: "+a.permissions+" (this session only).\n")
		if a.permissions == "automatic" {
			a.write(colourOrange, "AUTOMATIC MODE: pending and future model commands run with your account rights without approval.\n")
		}
	case "/new":
		if !allowNew {
			a.write(colourWhite, "Use /new at the portal prompt after this turn ends.\n")
			break
		}
		clear(a.history[2:])
		a.history = a.history[:2]
		a.write(colourWhite, "Conversation cleared. Session statistics and permissions retained.\n")
	case "/exit":
		return true, true
	default:
		a.write(colourWhite, "Unknown session command. Use /help.\n")
	}
	return true, false
}

func explainError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "turn cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "model request timed out; check the inference server or request_timeout_seconds"
	}
	return err.Error()
}
