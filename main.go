package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

var version = "1.0.0-rc.6"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Portal:", terminalText(explainError(err)))
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	return runInput(args, os.Stdin, out, errOut)
}

func runInput(args []string, input io.Reader, out, errOut io.Writer) error {
	f := flag.NewFlagSet("portal", flag.ContinueOnError)
	f.SetOutput(errOut)
	configOverride := f.String("config", "", "config file (default: portal.json beside the executable)")
	cwdOverride := f.String("cwd", "", "working folder for local commands")
	initialPrompt := f.String("prompt", "", "first session prompt (quote the text; put other options first)")
	showVersion := f.Bool("version", false, "show the build version")
	startupCheck := f.Bool("startup-check", false, "check local startup without inference")
	check := f.Bool("check", false, "check startup and model text reply; no tools are run")
	toolCheck := f.Bool("tool-check", false, "check model tools and local PowerShell with one fixed marker command")
	f.Usage = func() {
		fmt.Fprintln(f.Output(), "Portal - Windows support agent")
		fmt.Fprintln(f.Output(), "Usage: portal.exe [--config file] [--cwd folder] [--prompt text | --check | --tool-check | --startup-check | --version]")
		f.PrintDefaults()
	}
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	promptSet := false
	f.Visit(func(option *flag.Flag) {
		if option.Name == "prompt" {
			promptSet = true
		}
	})
	if f.NArg() != 0 && !promptSet {
		return fmt.Errorf("unexpected argument; use --help for valid options")
	}
	if promptSet {
		*initialPrompt = strings.TrimSpace(strings.Join(append([]string{*initialPrompt}, f.Args()...), " "))
		if *initialPrompt == "" {
			return fmt.Errorf("--prompt requires nonempty text")
		}
		if len(*initialPrompt) > maxCommandBytes {
			return fmt.Errorf("--prompt exceeds 64 KiB")
		}
		if *startupCheck || *check || *toolCheck || *showVersion {
			return fmt.Errorf("--prompt cannot be combined with a check or --version")
		}
	}
	checks := 0
	for _, enabled := range []bool{*startupCheck, *check, *toolCheck} {
		if enabled {
			checks++
		}
	}
	if checks > 1 {
		return fmt.Errorf("use only one check option")
	}
	if *showVersion {
		fmt.Fprintln(out, version)
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate the executable: %w", err)
	}
	path, err := configPath(executable, *configOverride)
	if err != nil {
		return fmt.Errorf("cannot resolve config path: %w", err)
	}
	c, err := loadConfig(path)
	if err != nil {
		return err
	}
	cwd, err := workingDirectory(*cwdOverride)
	if err != nil {
		return err
	}
	identity, err := workstationIdentity()
	if err != nil {
		return fmt.Errorf("cannot inspect the local workstation: %w", err)
	}
	if _, err := powershellPath(); err != nil {
		return err
	}
	colour, restore := enableColour(out)
	defer restore()
	fmt.Fprint(out, terminalText(fmt.Sprintf("Portal %s\n%s\nConfig: %s\nWorking folder: %s\nEndpoint: %s\nModel: %s\nExecution mode: %s\n",
		version, identity, path, cwd, c.BaseURL, c.Model, c.ApprovalMode)))
	if *startupCheck {
		fmt.Fprintln(out, "Startup check passed. No inference or commands were run.")
		return nil
	}
	client := newModelClient(c)
	defer client.close()
	console := newConsole(input)
	defer console.close()
	ctx, done := console.startTurn()
	metadataErr := client.discoverContext(ctx)
	interrupted := ctx.Err()
	done()
	if interrupted != nil {
		return interrupted
	}
	if metadataErr != nil {
		fmt.Fprintln(out, "Model context: not reported; the server enforces its token limit.")
	} else {
		fmt.Fprintf(out, "Model context: %d tokens (reported by server)\n", client.contextTokens)
	}
	a := newAgent(client, identity, cwd, out)
	a.colour = colour
	if *toolCheck {
		ctx, done := console.startTurn()
		defer done()
		return a.toolCheck(ctx)
	}
	if *check {
		ctx, done := console.startTurn()
		defer done()
		onThinking, finishThinking := a.thinkingStream()
		client.onThinking = onThinking
		m, err := client.complete(ctx, []message{{Role: "system", Content: c.SystemPrompt}, {Role: "user", Content: "This is a connection check. Reply with PORTAL_OK only. Do not use tools."}}, false)
		finishThinking()
		if err != nil {
			return err
		}
		if m.Thinking != "" {
			a.write(colourGrey, a.redact(m.Thinking)+"\n")
		}
		if strings.TrimSpace(m.Content) != "PORTAL_OK" || m.Refusal != "" {
			return fmt.Errorf("model text check failed: expected PORTAL_OK; check the model and prompt")
		}
		a.write(colourWhite, "PORTAL_OK\n")
		fmt.Fprintln(out, "Model text check passed. Tool support is not checked.")
		return nil
	}
	if c.ApprovalMode == "automatic" {
		fmt.Fprintln(out, "AUTOMATIC MODE: model commands run with your account rights without approval.")
	}
	return interact(console, a, *initialPrompt)
}
