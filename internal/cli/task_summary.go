package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/instaclustr/insta-cli/internal/config"
	"github.com/instaclustr/insta-cli/internal/manifest"
	"github.com/instaclustr/insta-cli/internal/narration"
	"github.com/instaclustr/insta-cli/internal/state"
)

// Task summaries are commentary only. They cannot change the selected command,
// approvals, recorded status, verification policy, or the CLI's exit code.
type taskNarrator struct {
	s        *session
	client   *narration.Client
	evidence taskEvidence
	fallback string
}

type taskEvidence struct {
	Lab                    string            `json:"lab"`
	Task                   string            `json:"task"`
	Title                  string            `json:"title"`
	LearningGoal           string            `json:"learning_goal"`
	Operation              string            `json:"operation"`
	Mode                   string            `json:"mode"`
	DeclaredEffects        []string          `json:"declared_effects"`
	VerificationPolicy     string            `json:"verification_policy"`
	ObservedResult         *taskResultDetail `json:"observed_result,omitempty"`
	DeclaredCommand        []string          `json:"declared_command"`
	WorkingDirectory       string            `json:"working_directory"`
	BlueprintExcerpt       string            `json:"blueprint_excerpt,omitempty"`
	BlueprintExcerptNotice string            `json:"blueprint_excerpt_notice,omitempty"`
}

type taskResultDetail struct {
	Status       string `json:"status"`
	ExitCode     *int   `json:"exit_code"`
	DurationMS   int64  `json:"duration_ms"`
	Verification string `json:"recorded_verification"`
	OutputTail   string `json:"output_tail"`
	OutputNotice string `json:"output_notice"`
}

func (s *session) newTaskNarrator(taskID, operation string, task manifest.Task, op manifest.Operation) *taskNarrator {
	// Include CLI-only summary credentials in the same redaction boundary as
	// lab credentials, even for callers that construct a session directly.
	s.Redactor = s.Redactor.WithSecrets(config.SecretValues(s.Manifest, s.Values))
	mode := op.Mode
	if mode == "" {
		mode = "oneshot"
	}
	displayTask := task
	displayTask.Name = s.redactAgent(task.Name)
	displayTask.Description = s.redactAgent(task.Description)
	displayTask.Blueprint = s.redactAgent(task.Blueprint)
	goal := s.taskFeedback(taskDescription(s.Manifest.ID, taskID, displayTask))
	effects := make([]string, len(op.Effects))
	for i, effect := range op.Effects {
		effects[i] = s.redactAgent(effect)
	}
	command := make([]string, len(op.Command))
	for i, arg := range op.Command {
		command[i] = s.redactAgent(arg)
	}
	cwd := op.Cwd
	if cwd == "" {
		cwd = task.Source
	}
	n := &taskNarrator{
		s: s,
		evidence: taskEvidence{
			Lab: s.redactAgent(s.Manifest.ID), Task: s.redactAgent(taskID), Title: taskTitle(s.Manifest.ID, taskID, displayTask),
			LearningGoal: goal, Operation: operation, Mode: mode,
			DeclaredEffects: effects, VerificationPolicy: s.redactAgent(op.Verification),
			DeclaredCommand: command, WorkingDirectory: s.redactAgent(cwd),
		},
		fallback: goal + " Selected operation: " + operation + " (" + mode + ").",
	}
	cfg, err := narration.ConfigFromValues(s.Values)
	if !cfg.Enabled {
		return n
	}
	if err != nil {
		fmt.Fprintln(s.App.Err, s.taskFeedback("Model summaries unavailable: "+err.Error()+". Using task metadata and recorded results."))
		return n
	}
	n.client = &narration.Client{Config: cfg, HTTP: s.App.SummaryClient}
	n.evidence.BlueprintExcerpt, n.evidence.BlueprintExcerptNotice = s.taskBlueprintExcerpt(task.Blueprint)
	return n
}

func (n *taskNarrator) before(ctx context.Context) {
	if !n.describe(ctx, "before") {
		fmt.Fprintln(n.s.App.Err, "Before: "+n.s.taskFeedback(n.fallback))
	}
}

func (n *taskNarrator) after(ctx context.Context, result state.Operation, durationMS int64) {
	code := "unavailable"
	if result.ExitCode != nil {
		code = fmt.Sprint(*result.ExitCode)
	}
	fmt.Fprintln(n.s.App.Err, n.s.taskFeedback(fmt.Sprintf("Result: %s/%s | %s | exit %s | %s", result.Task, result.Name, result.Status, code, (time.Duration(durationMS)*time.Millisecond).Round(time.Millisecond))))
	n.evidence.ObservedResult = &taskResultDetail{
		Status: result.Status, ExitCode: result.ExitCode, DurationMS: durationMS,
		Verification: n.s.redactAgent(result.Verification), OutputTail: n.s.redactAgent(result.OutputTail),
		OutputNotice: "Last up to 8192 bytes of this invocation only; earlier output may be omitted. Empty output is not verification. Prior invocations and appended log history are not included.",
	}
	if !n.describe(ctx, "after") {
		if result.Status == "failed" {
			fmt.Fprintln(n.s.App.Err, "After: The operation did not complete successfully. Review its output and log before continuing this exercise.")
		} else {
			fmt.Fprintln(n.s.App.Err, "After: The declared operation finished. Check its evidence against the verification note below before moving to the next exercise.")
		}
	}
	fmt.Fprintln(n.s.App.Err, "Verification: "+n.s.taskFeedback(result.Verification))
	fmt.Fprintln(n.s.App.Err, "Log: "+n.s.taskFeedback(result.Log))
}

func (n *taskNarrator) describe(ctx context.Context, phase string) bool {
	if n.client == nil || ctx.Err() != nil {
		return false
	}
	label := "Before"
	if phase == "after" {
		label = "After"
	}
	fmt.Fprintln(n.s.App.Err, n.s.taskFeedback(fmt.Sprintf("%s summary (OpenAI / %s):", label, n.client.Config.Model)))
	evidence, err := json.Marshal(n.evidence)
	if err != nil {
		return false
	}
	text, err := n.client.Summarize(ctx, phase, string(evidence), n.s.redactAgent)
	if err != nil {
		if ctx.Err() == nil {
			fmt.Fprintln(n.s.App.Err, n.s.taskFeedback("  Model summary unavailable: "+err.Error()+". Using recorded task information."))
		}
		return false
	}
	fmt.Fprintln(n.s.App.Err, "  "+n.s.taskFeedback(text))
	return true
}

func (s *session) taskFeedback(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s.redactAgent(text))
	return strings.Join(strings.Fields(text), " ")
}

// Read only the selected declared blueprint. Oversized or nonregular files are
// omitted, never traversed as arbitrary project context. Redact the complete
// bounded file before truncating so a credential cannot be split at the limit.
func (s *session) taskBlueprintExcerpt(relative string) (string, string) {
	if !sourceAllowed(s.Manifest, relative) {
		return "", "Selected blueprint is outside approved teaching material; use the supplied task metadata."
	}
	path, err := manifest.Path(s.Root, relative, false)
	if err != nil {
		return "", "Selected blueprint was unavailable; use the supplied task metadata."
	}
	root, err := filepath.EvalSymlinks(s.Root)
	if err != nil {
		return "", "Selected blueprint was unavailable; use the supplied task metadata."
	}
	resolved, err := filepath.Rel(root, path)
	if err != nil || !sourceAllowed(s.Manifest, filepath.ToSlash(resolved)) {
		return "", "Selected blueprint is outside approved teaching material; use the supplied task metadata."
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return "", "Selected blueprint was omitted; use the supplied task metadata."
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "Selected blueprint was unavailable; use the supplied task metadata."
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(b) > 64<<10 || bytesContainNUL(b) || !utf8.Valid(b) {
		return "", "Selected blueprint was omitted; use the supplied task metadata."
	}
	text := s.redactAgent(strings.ToValidUTF8(string(b), ""))
	const limit = 6 << 10
	if len(text) > limit {
		text = text[:limit]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		return text, "Excerpt limited to the first 6144 bytes of the redacted selected blueprint."
	}
	return text, "Only the selected task blueprint is included."
}
