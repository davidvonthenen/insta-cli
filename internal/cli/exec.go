package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/instaclustr/insta-cli/internal/coding"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const maxExecPrompt = 8192

type execMode int

const (
	execLoop execMode = iota
	execInteractive
	execPrompt
)

type execOptions struct {
	mode        execMode
	instruction string
	taskID      string
	profile     string
	approve     bool
}

func (a *App) addExec(root *cobra.Command) {
	var options execOptions
	var interactive bool
	c := &cobra.Command{
		Use:   "exec [-p message | -i [instruction]]",
		Short: "Chat in the lab, submit a prompt, or open the Codex terminal",
		Long: "Work with Codex in the current lab project using its declared task instructions and configured environment.\n\n" +
			"Modes:\n" +
			"  insta exec                  Continue a conversation, one message per line.\n" +
			"  insta exec -p \"message\"     Print one response and return to your shell.\n" +
			"  insta exec -i               Open the native Codex terminal.\n\n" +
			"Use --task to focus the conversation on an exercise. The default conversation\n" +
			"ends with /quit, /exit, or EOF. Session details and progress go to stderr;\n" +
			"assistant responses go to stdout.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.JSON {
				return failure("usage", 2, errors.New("insta exec does not support --json"))
			}
			switch {
			case cmd.Flags().Changed("prompt"):
				if interactive || len(args) != 0 || strings.TrimSpace(options.instruction) == "" {
					return failure("usage", 2, errors.New("use -p with one nonempty message and without -i or a positional instruction"))
				}
				options.mode = execPrompt
			case interactive:
				options.mode = execInteractive
				if len(args) != 0 {
					options.instruction = args[0]
				}
			case len(args) != 0:
				return failure("usage", 2, errors.New("use -p MESSAGE for one response or -i [instruction] for the native Codex terminal"))
			}
			if cmd.Flags().Changed("profile") && strings.TrimSpace(options.profile) == "" {
				return failure("usage", 2, errors.New("--profile requires a Codex profile name"))
			}
			ctx := cmd.Context()
			if options.mode != execInteractive {
				var stop context.CancelFunc
				ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
				defer stop()
			}
			err := a.withSession(ctx, false, func(s *session) error { return s.Exec(ctx, options) })
			if ctx.Err() != nil {
				return failure("interrupted", 130, ctx.Err())
			}
			return err
		},
	}
	c.Flags().BoolVarP(&interactive, "interactive", "i", false, "Open the native Codex terminal")
	c.Flags().StringVarP(&options.instruction, "prompt", "p", "", "Submit one message and print the response")
	c.Flags().StringVar(&options.taskID, "task", "", "Select a declared teaching task for this session")
	c.Flags().StringVar(&options.profile, "profile", "", "Use an existing Codex configuration profile")
	c.Flags().BoolVar(&options.approve, "approve", false, "Enable workspace writes and network access without Codex shell prompts, and preapprove declared task effects in this project and stack")
	root.AddCommand(c)
}

// Session context is diagnostic output: piping a one-shot answer must never
// capture headings, progress, or completion notices as part of that answer.
func (s *session) describeExec(adapter coding.CodingCLI, options execOptions, req coding.SessionRequest) {
	mode, guidance := "conversation", "Enter one message per line; /quit, /exit, or EOF ends this session."
	switch options.mode {
	case execPrompt:
		mode, guidance = "one-shot", "Submit one prompt, print the response, and return to your shell."
	case execInteractive:
		mode, guidance = "native terminal", "Opening the Codex terminal. Exit Codex to return to your shell."
	}
	task, profile := req.TaskID, req.Profile
	if task == "" {
		task = "whole lab"
	}
	if profile == "" {
		profile = "default"
	}
	for _, line := range []string{
		fmt.Sprintf("Session: %s | mode: %s", adapter.Name(), mode),
		fmt.Sprintf("Project: %s | lab: %s | stack: %s", s.Root, s.Project.Lab, s.Project.SelectedStack),
		fmt.Sprintf("Context: %s | profile: %s", task, profile),
		guidance,
	} {
		fmt.Fprintln(s.App.Err, s.execDisplay(line))
	}
	if options.approve {
		fmt.Fprintln(s.App.Err, "Approval: declared task effects are preapproved for this project and stack.")
	}
}

func (s *session) execDisplay(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s.redactAgent(text))
}

func (s *session) runPrompt(ctx context.Context, adapter coding.CodingCLI, req coding.SessionRequest, thread string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if thread == "" {
		fmt.Fprintln(s.App.Err, "Working on your request...")
	} else {
		fmt.Fprintln(s.App.Err, "Continuing the conversation...")
	}
	if errFile, ok := s.App.Err.(*os.File); ok && term.IsTerminal(int(errFile.Fd())) {
		stop := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			start := time.Now()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					fmt.Fprintf(errFile, "Still working (%s elapsed)...\n", time.Since(start).Round(time.Second))
				}
			}
		}()
		defer func() { close(stop); <-finished }()
	}
	id, err := adapter.RunPrompt(ctx, req, thread)
	return id, codingExit(err)
}

func codingExit(err error) error {
	var child *coding.ChildExitError
	if errors.As(err, &child) {
		code := child.Code
		if code < 1 || code > 255 {
			code = 1
		}
		return &Error{Kind: "coding_cli_exit", Message: child.Error(), Code: code, ChildExitCode: &child.Code}
	}
	return err
}

func (s *session) promptLoop(ctx context.Context, adapter coding.CodingCLI, req coding.SessionRequest) error {
	in := s.App.In
	if f, ok := in.(*os.File); ok {
		in = execInput{ctx, f}
	}
	input := bufio.NewReader(in)
	terminal := isTTY(s.App.In)
	var thread string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if terminal {
			fmt.Fprint(s.App.Err, " ")
			fmt.Fprint(s.App.Err, "insta> ")
		}
		line, err := readExecLine(input)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, errPromptTooLong) {
			fmt.Fprintln(s.App.Err, err)
			continue
		}
		if err != nil {
			return err
		}
		switch strings.TrimSpace(line) {
		case "":
			continue
		case "/quit", "/exit":
			return nil
		}
		req.Instruction = s.redactAgent(line)
		next, err := s.runPrompt(ctx, adapter, req, thread)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			fmt.Fprintln(s.App.Err, "Error:", s.redactAgent(err.Error()))
			if terminal {
				fmt.Fprintln(s.App.Err, "Enter another message to retry, or /quit to leave.")
			}
			continue
		}
		thread = next
	}
}

var errPromptTooLong = fmt.Errorf("prompt exceeds %d bytes", maxExecPrompt)

// Drain an oversized line before prompting again. The remaining bytes in the
// same pasted message must never become instructions on a later turn.
func readExecLine(input *bufio.Reader) (string, error) {
	var line strings.Builder
	tooLong := false
	for {
		part, more, err := input.ReadLine()
		if err != nil && !(errors.Is(err, io.EOF) && (line.Len() > 0 || tooLong)) {
			return "", err
		}
		if line.Len()+len(part) > maxExecPrompt {
			tooLong = true
		}
		if !tooLong {
			line.Write(part)
		}
		if !more {
			if tooLong {
				return "", errPromptTooLong
			}
			return line.String(), nil
		}
	}
}

// A terminal or a pipe can remain idle indefinitely. Poll before a blocking
// read so context cancellation also works mid-line, without closing borrowed
// stdin or leaving a goroutine behind to consume another command's input.
type execInput struct {
	ctx context.Context
	f   *os.File
}

func (in execInput) Read(p []byte) (int, error) {
	descriptors := []unix.PollFd{{Fd: int32(in.f.Fd()), Events: unix.POLLIN}}
	for in.ctx.Err() == nil {
		n, err := unix.Poll(descriptors, 200)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n != 0 {
			return in.f.Read(p)
		}
	}
	return 0, in.ctx.Err()
}
