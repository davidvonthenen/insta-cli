package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/instaclustr/insta-cli/internal/manifest"
	"github.com/instaclustr/insta-cli/internal/state"
)

func taskSummaryFixture(t *testing.T, script string, handler http.HandlerFunc) (*session, *httptest.Server) {
	t.Helper()
	s := agentFixture(t)
	task := s.Manifest.Tasks["example"]
	task.Name = "Example exercise"
	task.Description = "Compare observed evidence with the exercise's expected outcome."
	task.Operations = map[string]manifest.Operation{"validate": {
		Command:      []string{"python", "-m", "src.validate"},
		Verification: "The program exited; this does not independently establish answer quality.",
	}}
	s.Manifest.Tasks["example"] = task
	taskSealFixture(t, s, script)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	s.Values["summary.enabled"] = "true"
	s.Values["summary.provider"] = "openai"
	s.Values["summary.model"] = "fixture-summary-model"
	s.Values["summary.api_key"] = "fixture-summary-secret"
	s.Values["summary.base_url"] = server.URL
	s.App.SummaryClient = server.Client()
	return s, server
}

func summaryReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
		"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": text},
	}}})
}

func TestTaskSummaryClientOwnsOpenAIRequestsAndClaudeIsPlaceholder(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "claude"} {
		t.Run(provider, func(t *testing.T) {
			var calls atomic.Int32
			script := `
if [ "${INSTA_SUMMARY_API_KEY+set}" = set ] || [ "${INSTA_SUMMARY_BASE_URL+set}" = set ] || [ "${ANTHROPIC_API_KEY+set}" = set ]; then
  printf 'Summary-only configuration leaked into the lab process\n'
  exit 89
fi
printf 'LAB_PROGRAM_OUTPUT\n'
`
			s, server := taskSummaryFixture(t, script, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-summary-secret" {
					t.Error("summary did not use the OpenAI client")
				}
				summaryReply(w, "The CLI explains the declared exercise using its observed evidence.")
			})
			s.Values["summary.provider"] = provider
			s.Values["ANTHROPIC_API_KEY"] = "configured-anthropic-secret"
			// These are inherited shell settings as well as resolved CLI values.
			// The declared lab program must receive neither summary-only setting.
			t.Setenv("INSTA_SUMMARY_API_KEY", "fixture-summary-secret")
			t.Setenv("INSTA_SUMMARY_BASE_URL", server.URL)
			t.Setenv("ANTHROPIC_API_KEY", "configured-anthropic-secret")
			result, err := s.RunTask(context.Background(), "example", "validate")
			if err != nil || result.ExitCode == nil || *result.ExitCode != 0 || !strings.Contains(result.OutputTail, "LAB_PROGRAM_OUTPUT") {
				t.Fatalf("CLI summary provider affected the lab operation: %+v / %v", result, err)
			}
			wantRequests := int32(2)
			if provider != "openai" {
				wantRequests = 0
				diagnostic := s.App.Err.(*bytes.Buffer).String()
				if !strings.Contains(diagnostic, "placeholder") || !strings.Contains(diagnostic, "Before:") || !strings.Contains(diagnostic, "After:") {
					t.Fatalf("placeholder lacks clear factual fallback: %s", diagnostic)
				}
			}
			if calls.Load() != wantRequests {
				t.Fatalf("provider %s made %d requests, want %d", provider, calls.Load(), wantRequests)
			}
		})
	}
}

func TestTaskSummariesBracketExecutionAndUseCurrentRedactedEvidence(t *testing.T) {
	var calls atomic.Int32
	var s *session
	var confirmed atomic.Bool
	s, _ = taskSummaryFixture(t, "printf 'CURRENT_RUN fixture-secret fixture-summary-secret\\n'\ntouch ran.marker\n", func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-summary-secret" {
			t.Error("summary did not use the configured provider endpoint/authentication")
		}
		if !confirmed.Load() {
			t.Error("summary request occurred before task-effect approval")
		}
		for _, secret := range []string{"fixture-secret", "fixture-summary-secret", "fixture-management-secret"} {
			if bytes.Contains(body, []byte(secret)) {
				t.Errorf("known credential entered model context: %s", secret)
			}
		}
		if bytes.Contains(body, []byte("OLD_RUN_ONLY")) {
			t.Error("summary included historical appended log output")
		}
		_, markerErr := os.Stat(filepath.Join(s.Root, "scripts/example/ran.marker"))
		if n == 1 {
			if !errors.Is(markerErr, os.ErrNotExist) || !bytes.Contains(body, []byte("Summary phase: before")) || bytes.Contains(body, []byte("CURRENT_RUN")) {
				t.Error("before summary did not precede the declared operation")
			}
			summaryReply(w, "BEFORE_EXPLANATION fixture-summary-secret")
		} else {
			if markerErr != nil || !bytes.Contains(body, []byte("Summary phase: after")) || !bytes.Contains(body, []byte("CURRENT_RUN [redacted] [redacted]")) {
				t.Error("after summary did not use the current operation output")
			}
			persisted, err := state.Load(s.Root)
			if err != nil || persisted.Stack().Operations["example/validate"].Status != "exited" {
				t.Error("outcome was not durable before the after-summary request")
			}
			summaryReply(w, "AFTER_EXPLANATION fixture-secret")
		}
	})
	t.Setenv("INSTACLUSTR_PROVISION_API_KEY", "fixture-management-secret")
	task := s.Manifest.Tasks["example"]
	op := task.Operations["validate"]
	op.Effects = []string{"remote-model-call"}
	task.Operations["validate"] = op
	s.Manifest.Tasks["example"] = task
	taskSealFixture(t, s, "printf 'CURRENT_RUN fixture-secret fixture-summary-secret\\n'\ntouch ran.marker\n")
	agentWrite(t, s.Root, ".insta/logs/dev-example-validate.log", "OLD_RUN_ONLY\n")
	s.App.ConfirmFunc = func(string) error { confirmed.Store(true); return nil }
	var combined bytes.Buffer
	s.App.Out, s.App.Err = &combined, &combined
	result, err := s.RunTask(context.Background(), "example", "validate")
	if err != nil || calls.Load() != 2 || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("task/summaries failed: result=%+v err=%v calls=%d", result, err, calls.Load())
	}
	output := combined.String()
	before, child, after := strings.Index(output, "BEFORE_EXPLANATION"), strings.Index(output, "CURRENT_RUN"), strings.Index(output, "AFTER_EXPLANATION")
	if before < 0 || child <= before || after <= child {
		t.Fatalf("summary/process order incorrect: %s", output)
	}
	if strings.Contains(output, "fixture-secret") || strings.Contains(output, "fixture-summary-secret") || !strings.Contains(output, "Verification:") {
		t.Fatal("terminal feedback leaked a secret or lost verification limits")
	}
}

func TestTaskSummaryFailureDoesNotMaskChildOutcome(t *testing.T) {
	for _, exitCode := range []int{0, 23} {
		t.Run(fmt.Sprint(exitCode), func(t *testing.T) {
			var calls atomic.Int32
			s, _ := taskSummaryFixture(t, fmt.Sprintf("printf 'observed failure context\\n'\nexit %d\n", exitCode), func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				var body struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 2 {
					t.Error("invalid summary request evidence", err)
				} else if n == 2 {
					status := "exited"
					if exitCode != 0 {
						status = "failed"
					}
					for _, expected := range []string{`"status":"` + status + `"`, fmt.Sprintf(`"exit_code":%d`, exitCode), "observed failure context"} {
						if !strings.Contains(body.Messages[1].Content, expected) {
							t.Errorf("after-summary omitted actual outcome %q", expected)
						}
					}
				}
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, "fixture-summary-secret sensitive provider body")
			})
			result, err := s.RunTask(context.Background(), "example", "validate")
			if result.ExitCode == nil || *result.ExitCode != exitCode || calls.Load() != 2 {
				t.Fatalf("summary errors changed child result: %+v / %v", result, err)
			}
			if exitCode == 0 && err != nil {
				t.Fatal("summary failure failed an otherwise completed operation", err)
			}
			if exitCode != 0 {
				var ce *Error
				if !errors.As(err, &ce) || ce.Code != 6 || ce.ChildExitCode == nil || *ce.ChildExitCode != exitCode {
					t.Fatal("summary failure masked child failure", err)
				}
			}
			out := s.App.Err.(*bytes.Buffer).String()
			if !strings.Contains(out, "Model summary unavailable") || !strings.Contains(out, "Before:") || !strings.Contains(out, "After:") || strings.Contains(out, "sensitive provider body") {
				t.Fatalf("fallback missing or provider body leaked: %s", out)
			}
		})
	}
}

func TestTaskSummariesSkippedBeforePrerequisitesOrDeclinedEffects(t *testing.T) {
	for _, scenario := range []string{"missing-config", "declined-effects"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			s, _ := taskSummaryFixture(t, "touch ran.marker\n", func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				summaryReply(w, "Unexpected call")
			})
			task := s.Manifest.Tasks["example"]
			op := task.Operations["validate"]
			if scenario == "missing-config" {
				op.Required = []string{"OPENAI_API_KEY"}
				s.Values["OPENAI_API_KEY"] = ""
			} else {
				op.Effects = []string{"index-write"}
				s.App.ConfirmFunc = func(string) error { return errors.New("declined") }
			}
			task.Operations["validate"] = op
			s.Manifest.Tasks["example"] = task
			taskSealFixture(t, s, "touch ran.marker\n")
			if _, err := s.RunTask(context.Background(), "example", "validate"); err == nil {
				t.Fatal("operation bypassed its prerequisite or approval")
			}
			if calls.Load() != 0 {
				t.Fatal("summary API contacted for an operation that could not run")
			}
			if _, err := os.Stat(filepath.Join(s.Root, "scripts/example/ran.marker")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("task executed without prerequisites/approval")
			}
		})
	}
}

func TestTaskSummaryCancellationPreservesActualExecutionState(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			s, _ := taskSummaryFixture(t, "touch ran.marker\n", func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				n := calls.Add(1)
				if phase == "before" || n == 2 {
					cancel()
					<-r.Context().Done()
					return
				}
				summaryReply(w, "Planned work")
			})
			result, err := s.RunTask(ctx, "example", "validate")
			var ce *Error
			if !errors.As(err, &ce) || ce.Kind != "interrupted" || ce.Code != 130 {
				t.Fatalf("canceled summary has wrong CLI error: %v", err)
			}
			persisted, loadErr := state.Load(s.Root)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			_, markerErr := os.Stat(filepath.Join(s.Root, "scripts/example/ran.marker"))
			if phase == "before" {
				if calls.Load() != 1 || !errors.Is(markerErr, os.ErrNotExist) || result.ExitCode != nil || len(persisted.Stack().Operations) != 0 {
					t.Fatal("pre-summary cancellation started or fabricated an operation")
				}
			} else if calls.Load() != 2 || markerErr != nil || result.ExitCode == nil || *result.ExitCode != 0 || persisted.Stack().Operations["example/validate"].Status != "exited" {
				t.Fatal("post-summary cancellation erased or changed completed operation state")
			}
		})
	}
}

func TestTaskSummaryTimeoutContinuesAndJSONStaysStructured(t *testing.T) {
	var calls atomic.Int32
	s, server := taskSummaryFixture(t, "printf 'child evidence\\n'\n", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls.Add(1)
		<-r.Context().Done()
	})
	s.App.SummaryClient.Timeout = 25 * time.Millisecond
	t.Chdir(s.Root)
	for key, value := range map[string]string{
		"INSTA_SUMMARY_ENABLED": "true", "INSTA_SUMMARY_PROVIDER": "openai",
		"INSTA_SUMMARY_MODEL": "fixture-summary-model", "INSTA_SUMMARY_API_KEY": "fixture-summary-secret",
		"INSTA_SUMMARY_BASE_URL": server.URL,
	} {
		t.Setenv(key, value)
	}
	code := s.App.Execute(context.Background(), []string{"task", "run", "example", "validate", "--json"})
	if code != 0 || calls.Load() != 2 {
		t.Fatalf("summary timeout affected task: exit=%d calls=%d err=%s", code, calls.Load(), s.App.Err)
	}
	var envelope struct {
		OK   bool            `json:"ok"`
		Data state.Operation `json:"data"`
	}
	if err := json.Unmarshal(s.App.Out.(*bytes.Buffer).Bytes(), &envelope); err != nil || !envelope.OK || envelope.Data.ExitCode == nil || *envelope.Data.ExitCode != 0 {
		t.Fatalf("JSON result was polluted: %v / %s", err, s.App.Out)
	}
	diagnostic := s.App.Err.(*bytes.Buffer).String()
	if !strings.Contains(diagnostic, "insta CLI v") || !strings.Contains(diagnostic, "child evidence") || !strings.Contains(diagnostic, "Model summary unavailable") {
		t.Fatal("JSON diagnostics missing task evidence/fallback")
	}
}

func TestTaskRunSingleOperationAndNoSummary(t *testing.T) {
	var calls atomic.Int32
	s, server := taskSummaryFixture(t, "printf 'one declared operation\\n'\n", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		summaryReply(w, "Unexpected summary")
	})
	t.Chdir(s.Root)
	for key, value := range map[string]string{
		"INSTA_SUMMARY_ENABLED": "true", "INSTA_SUMMARY_PROVIDER": "openai",
		"INSTA_SUMMARY_MODEL": "fixture-summary-model", "INSTA_SUMMARY_API_KEY": "fixture-summary-secret",
		"INSTA_SUMMARY_BASE_URL": server.URL,
	} {
		t.Setenv(key, value)
	}
	if code := s.App.Execute(context.Background(), []string{"task", "run", "example", "--no-summary"}); code != 0 || calls.Load() != 0 {
		t.Fatalf("single operation or --no-summary failed: exit=%d calls=%d err=%s", code, calls.Load(), s.App.Err)
	}
	task := s.Manifest.Tasks["example"]
	task.Operations["ingest"] = manifest.Operation{Command: []string{"python", "-m", "src.ingest"}}
	s.Manifest.Tasks["example"] = task
	taskSealFixture(t, s, "touch should-not-run.marker\n")
	if code := s.App.Execute(context.Background(), []string{"task", "run", "example"}); code != 2 || calls.Load() != 0 || !strings.Contains(s.App.Err.(*bytes.Buffer).String(), "choose an operation") {
		t.Fatal("ambiguous operation did not fail with actionable choices")
	}
	if _, err := os.Stat(filepath.Join(s.Root, "scripts/example/should-not-run.marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ambiguous task executed an operation")
	}
}

func TestTaskSummaryRedactsBeforeJSONAndRestrictsBlueprint(t *testing.T) {
	s := agentFixture(t)
	secret := "quote-\"-backslash-\\-secret"
	s.Values["summary.api_key"] = secret
	task := s.Manifest.Tasks["example"]
	task.Name, task.Description = secret, secret
	op := task.Operations["validate"]
	op.Effects, op.Verification = []string{secret}, secret
	n := s.newTaskNarrator("example", "validate", task, op)
	b, err := json.Marshal(n.evidence)
	escaped, _ := json.Marshal(secret)
	if err != nil || bytes.Contains(b, escaped[1:len(escaped)-1]) || !bytes.Contains(b, []byte("[redacted]")) {
		t.Fatalf("escaped credential survived evidence preparation: %s / %v", b, err)
	}
	agentWrite(t, s.Root, ".env", "UNDECLARED_SECRET=must-not-be-sent\n")
	link := filepath.Join(s.Root, "tasks/linked.md")
	if err := os.Symlink(filepath.Join(s.Root, ".env"), link); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{".env", "tasks/linked.md"} {
		text, notice := s.taskBlueprintExcerpt(relative)
		if text != "" || notice == "" {
			t.Fatal("private configuration was included as blueprint context", relative)
		}
	}
	agentWrite(t, s.Root, "tasks/example.md", strings.Repeat("a", (6<<10)-5)+secret+" tail")
	text, notice := s.taskBlueprintExcerpt("tasks/example.md")
	if len(text) > 6<<10 || strings.Contains(text, "quote-") || !strings.Contains(notice, "6144") {
		t.Fatal("blueprint truncation exposed a partial credential")
	}
}
