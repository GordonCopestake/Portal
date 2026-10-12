package main

import (
	"context"
	"errors"
	"strings"
)

const toolCheckCommand = "[Console]::WriteLine('PORTAL_TOOL_OK')"

func (a *agent) toolCheck(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	a.permissions = "ask"
	a.client.config.MaxToolCallsPerTurn = 1
	executions := 0
	unexpected, verified := false, false
	a.approve = func(_ context.Context, command string) (bool, error) {
		if command != toolCheckCommand || executions != 0 {
			unexpected = true
			cancel()
			return false, nil
		}
		return true, nil
	}
	execute := a.execute
	a.execute = func(ctx context.Context, command string) toolResult {
		executions++
		result := execute(ctx, command)
		verified = result.ExitCode != nil && *result.ExitCode == 0 && result.Error == "" && !result.TimedOut && !result.Cancelled && !result.Truncated && strings.TrimSpace(result.Stdout) == "PORTAL_TOOL_OK"
		return result
	}
	err := a.turn(ctx, "This is a tool connection check. Call powershell_call exactly once with this exact command: "+toolCheckCommand+". Do not request any other command. After the result, report PORTAL_TOOL_OK from stdout.")
	if unexpected {
		return errors.New("tool check failed: model requested an extra or unsupported command; only the fixed marker is allowed")
	}
	if err != nil {
		return err
	}
	last := a.history[len(a.history)-1]
	if executions != 1 || !verified || last.Refusal != "" || !strings.Contains(last.Content, "PORTAL_TOOL_OK") {
		return errors.New("tool check failed: expected one successful marker command and a final evidence report")
	}
	a.write(colourWhite, "Model tool check passed. The fixed marker command ran locally.\n")
	return nil
}
