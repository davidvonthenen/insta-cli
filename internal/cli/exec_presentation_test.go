package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/instaclustr/insta-cli/internal/coding"
)

func TestExecFeedbackDescribesActualContextBeforeLaunch(t *testing.T) {
	for _, tc := range []struct {
		name, mode, instruction, input, guidance, completion string
		execMode                                             execMode
	}{
		{"one-shot", "one-shot", "explain", "", "return to your shell", "Response complete.", execPrompt},
		{"conversation", "conversation", "", "explain\nagain\n/exit\n", "one message per line", "Conversation ended.", execLoop},
		{"terminal", "native terminal", "explain", "", "Exit Codex", "Codex terminal closed.", execInteractive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := agentFixture(t)
			s.Project.Stack().ActiveTask = "example"
			s.App.In = strings.NewReader(tc.input)
			diagnostic := s.App.Err.(*bytes.Buffer)
			out := s.App.Out.(*bytes.Buffer)
			calls := 0
			check := func(req coding.SessionRequest) error {
				calls++
				for _, expected := range []string{
					"Session: fake | mode: " + tc.mode,
					"Project: " + s.Root,
					"lab: rag-retrieval | stack: dev",
					"Context: example | profile: teaching",
					tc.guidance,
				} {
					if !strings.Contains(diagnostic.String(), expected) {
						t.Errorf("launch omitted %q from context: %s", expected, diagnostic)
					}
				}
				if strings.Contains(diagnostic.String(), tc.completion) {
					t.Error("session was reported complete before its child returned")
				}
				if req.TaskID != "example" || req.Profile != "teaching" {
					t.Errorf("displayed context disagrees with request: %+v", req)
				}
				fmt.Fprintln(req.Stdout, "answer")
				return nil
			}
			s.App.CodingCLI = &agentFakeCoding{
				readiness: coding.Readiness{State: coding.Ready, Configured: true},
				check:     check,
				prompt: func(_ context.Context, req coding.SessionRequest, _ string) (string, error) {
					return "same-thread", check(req)
				},
			}
			if err := s.Exec(context.Background(), execOptions{mode: tc.execMode, instruction: tc.instruction, profile: "teaching"}); err != nil {
				t.Fatal(err)
			}
			if out.String() != strings.Repeat("answer\n", calls) {
				t.Fatalf("session feedback polluted stdout: %q", out)
			}
			if strings.Count(diagnostic.String(), tc.completion) != 1 {
				t.Errorf("completion notice missing or repeated: %s", diagnostic)
			}
			if tc.execMode == execLoop && !strings.Contains(diagnostic.String(), "Continuing the conversation...") {
				t.Error("follow-up did not describe continuing the active conversation")
			}
		})
	}
}

func TestExecFeedbackDoesNotClaimCompletionOnFailure(t *testing.T) {
	for _, mode := range []execMode{execPrompt, execInteractive} {
		s := agentFixture(t)
		childErr := &coding.ChildExitError{Code: 42}
		s.App.CodingCLI = &agentFakeCoding{
			readiness: coding.Readiness{State: coding.Ready, Configured: true},
			check:     func(coding.SessionRequest) error { return childErr },
			prompt: func(context.Context, coding.SessionRequest, string) (string, error) {
				return "", childErr
			},
		}
		err := s.Exec(context.Background(), execOptions{mode: mode, instruction: "explain"})
		var child *Error
		if !errors.As(err, &child) || child.Code != 42 || child.ChildExitCode == nil || *child.ChildExitCode != 42 {
			t.Fatalf("mode %v lost child status: %v", mode, err)
		}
		diagnostic := s.App.Err.(*bytes.Buffer).String()
		if strings.Contains(diagnostic, "Response complete") || strings.Contains(diagnostic, "terminal closed") {
			t.Errorf("mode %v claimed completion on failure: %s", mode, diagnostic)
		}
	}
}

func TestExecFeedbackRedactsValuesAndControls(t *testing.T) {
	s := agentFixture(t)
	s.Root += "/fixture-secret\x1b[2J"
	s.App.CodingCLI = &agentFakeCoding{
		readiness: coding.Readiness{State: coding.Ready, Configured: true},
		prompt: func(context.Context, coding.SessionRequest, string) (string, error) {
			return "thread", nil
		},
	}
	if err := s.Exec(context.Background(), execOptions{mode: execPrompt, instruction: "explain", profile: "fixture-secret\rhidden"}); err != nil {
		t.Fatal(err)
	}
	diagnostic := s.App.Err.(*bytes.Buffer).String()
	if strings.Contains(diagnostic, "fixture-secret") || strings.ContainsAny(diagnostic, "\x1b\r") {
		t.Fatalf("session feedback exposed credential or terminal controls: %q", diagnostic)
	}
	if !strings.Contains(diagnostic, "Context: whole lab | profile: [redacted] hidden") {
		t.Fatalf("session defaults or sanitization missing: %q", diagnostic)
	}
}
