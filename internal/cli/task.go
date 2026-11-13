package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/instaclustr/insta-cli/internal/coding"
	"github.com/instaclustr/insta-cli/internal/config"
	"github.com/instaclustr/insta-cli/internal/manifest"
	"github.com/instaclustr/insta-cli/internal/runner"
	"github.com/instaclustr/insta-cli/internal/state"
	"github.com/spf13/cobra"
)

func (a *App) addTasks(root *cobra.Command) {
	tasks := &cobra.Command{Use: "task", Short: "Explore lab exercises and run their declared operations"}
	tasks.AddCommand(&cobra.Command{Use: "list", Short: "Show exercises, learning goals, and available operations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return a.withSession(cmd.Context(), false, func(s *session) error {
			return a.Result(s.Manifest.Tasks, formatTaskList(s))
		})
	}})
	tasks.AddCommand(&cobra.Command{Use: "start <task>", Short: "Select an exercise and start its required services", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return a.withSession(cmd.Context(), true, func(s *session) error {
			t, ok := s.Manifest.Tasks[args[0]]
			if !ok {
				return unknownTask(args[0])
			}
			if e := s.ApproveManifest(); e != nil {
				return e
			}
			if e := s.EnsureRuntime(cmd.Context()); e != nil {
				return e
			}
			if e := s.CheckPlatforms(cmd.Context(), t.Requires.Platforms); e != nil {
				return e
			}
			for _, name := range append(append([]string{}, t.Requires.Services...), t.Services...) {
				if e := s.StartService(cmd.Context(), name, args[0]); e != nil {
					return e
				}
			}
			s.Project.Stack().ActiveTask = args[0]
			if e := s.Save(); e != nil {
				return e
			}
			return a.Result(map[string]string{"task": args[0], "blueprint": t.Blueprint}, "Selected "+args[0]+". Read "+t.Blueprint+"; use insta task run to execute an operation.")
		})
	}})
	var noSummary bool
	run := &cobra.Command{Use: "run <task> [operation]", Short: "Explain, execute, and summarize one declared task operation", Long: `Run one operation from the selected lab's task manifest. Model summaries explain
the planned work and interpret the recorded result. The task's exit code and
verification evidence remain authoritative.

Omit the operation only when the task declares exactly one. Configure summary.*
with insta config, or use --no-summary for feedback without model calls.`, Example: "  insta task run baseline-vector-rag ingest\n  insta task run baseline-vector-rag query\n  insta task run setup-environment\n  insta task run baseline-vector-rag validate --no-summary", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		return a.withSession(cmd.Context(), true, func(s *session) error {
			task, ok := s.Manifest.Tasks[args[0]]
			if !ok {
				return unknownTask(args[0])
			}
			operation := ""
			if len(args) == 2 {
				operation = args[1]
			} else if len(task.Operations) == 1 {
				operation = manifest.Keys(task.Operations)[0]
			} else {
				return failure("usage", 2, fmt.Errorf("choose an operation for %s: %s; use insta task run %s <operation>", args[0], strings.Join(operationNames(task), ", "), args[0]))
			}
			if noSummary {
				s.Values["summary.enabled"] = "false"
			}
			if inherited := os.Getenv(coding.TaskApprovalEnv); inherited != "" {
				scope, err := s.taskApprovalScope()
				if err != nil {
					return err
				}
				s.taskEffectsPreapproved = inherited == scope
			}
			result, e := s.RunTask(cmd.Context(), args[0], operation)
			if e != nil {
				return e
			}
			return a.Result(result, "")
		})
	}}
	run.Flags().BoolVar(&noSummary, "no-summary", false, "Use recorded task feedback without calling a summary model")
	tasks.AddCommand(run)
	tasks.AddCommand(&cobra.Command{Use: "stop <task>", Short: "End an exercise and stop services it exclusively owns", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return a.withSession(cmd.Context(), true, func(s *session) error {
			if _, ok := s.Manifest.Tasks[args[0]]; !ok {
				return unknownTask(args[0])
			}
			// Stop consumers before their dependencies; shared/stack services remain.
			order := serviceOrder(s.Manifest)
			for i := len(order) - 1; i >= 0; i-- {
				name := order[i]
				service := s.Project.Stack().Services[name]
				if service != nil && service.Owner == args[0] {
					if e := s.StopService(cmd.Context(), name); e != nil {
						return e
					}
				}
			}
			if s.Project.Stack().ActiveTask == args[0] {
				s.Project.Stack().ActiveTask = ""
			}
			if e := s.Save(); e != nil {
				return e
			}
			return a.Result(map[string]string{"task": args[0]}, "Task context ended; its exclusive services were stopped. Shared support services remain owned by the stack.")
		})
	}})
	tasks.AddCommand(&cobra.Command{Use: "status [task]", Short: "Inspect recorded operation results and service readiness", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return a.withSession(cmd.Context(), false, func(s *session) error {
			filter := ""
			if len(args) > 0 {
				filter = args[0]
				if _, ok := s.Manifest.Tasks[filter]; !ok {
					return unknownTask(filter)
				}
			}
			ops := map[string]state.Operation{}
			for k, v := range s.Project.Stack().Operations {
				if filter == "" || v.Task == filter {
					ops[k] = v
				}
			}
			services := map[string]any{}
			for name, record := range s.Project.Stack().Services {
				status, e := runner.Control(cmd.Context(), record, "status")
				ready := false
				if e == nil && status.Running {
					if def, ok := s.Manifest.Services[name]; ok {
						ready = runner.CheckProbe(cmd.Context(), def.Probe) == nil
					}
				}
				services[name] = map[string]any{"running": e == nil && status.Running, "probe_passed": ready, "owner": record.Owner, "log": record.Log}
			}
			data := map[string]any{"active_task": s.Project.Stack().ActiveTask, "runtime": s.Project.Stack().RuntimeStatus, "services": services, "operations": ops}
			b, _ := json.MarshalIndent(data, "", "  ")
			return a.Result(data, string(b))
		})
	}})
	root.AddCommand(tasks)
}
func unknownTask(task string) error {
	return failure("configuration", 2, fmt.Errorf("unknown task %q; use insta task list", task))
}

// A parent and a nested CLI can discover the same project using different
// symlink aliases. Resolve the physical root before comparing their scopes.
func (s *session) taskApprovalScope() (string, error) {
	root, err := filepath.EvalSymlinks(s.Root) // project discovery supplies an absolute path
	if err != nil {
		return "", fmt.Errorf("resolve project for task approval: %w", err)
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%q %q %q %q", root, s.Project.ID, s.Project.SelectedStack, s.Hash)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *session) RunTask(ctx context.Context, taskID, operation string) (state.Operation, error) {
	var result state.Operation
	if ctx.Err() != nil {
		return result, failure("interrupted", 130, errors.New("task canceled before the operation started"))
	}
	s.Redactor = s.Redactor.WithSecrets(config.SecretValues(s.Manifest, s.Values))
	t, ok := s.Manifest.Tasks[taskID]
	if !ok {
		return result, unknownTask(taskID)
	}
	op, ok := t.Operations[operation]
	if !ok {
		return result, failure("configuration", 2, fmt.Errorf("unknown operation %q for %s; available operations: %s", operation, taskID, strings.Join(operationNames(t), ", ")))
	}
	if op.Mode == "interactive" && (!isTTY(s.App.In) || s.App.JSON) {
		return result, failure("terminal_required", 2, errors.New("this task operation is interactive; use a terminal without --json"))
	}
	displayTask := t
	displayTask.Name = s.redactAgent(t.Name)
	fmt.Fprintln(s.App.Err, s.taskFeedback(fmt.Sprintf("Task: %s / %s | %s | stack: %s", taskID, operation, taskTitle(s.Manifest.ID, taskID, displayTask), s.Project.SelectedStack)))
	fmt.Fprintln(s.App.Err, "Checking configuration, runtime, and task prerequisites...")
	if e := s.ApproveManifest(); e != nil {
		return result, e
	}
	if e := config.Require(s.Values, append(manifest.Required(t.Environment, operation, s.Values), op.Required...)...); e != nil {
		return result, failure("configuration", 2, e)
	}
	if e := s.EnsureRuntime(ctx); e != nil {
		return result, e
	}
	if e := s.CheckPlatforms(ctx, append(append([]string{}, t.Requires.Platforms...), op.Requires.Platforms...)); e != nil {
		return result, e
	}
	for _, name := range append(append([]string{}, t.Requires.Services...), op.Requires.Services...) {
		record := s.Project.Stack().Services[name]
		if record == nil {
			return result, failure("prerequisite", 3, fmt.Errorf("service %s is required; run insta task start %s", name, taskID))
		}
		status, e := runner.Control(ctx, record, "status")
		if e != nil || !status.Running {
			return result, failure("prerequisite", 3, fmt.Errorf("service %s is not running; run insta task start %s", name, taskID))
		}
		if e := runner.CheckProbe(ctx, s.Manifest.Services[name].Probe); e != nil {
			return result, failure("prerequisite", 3, e)
		}
	}
	for _, index := range op.Indexes {
		if e := s.checkIndex(ctx, index); e != nil {
			return result, failure("prerequisite", 3, e)
		}
	}
	if len(op.Effects) > 0 {
		fmt.Fprintln(s.App.Err, s.taskFeedback("Effects: "+strings.Join(op.Effects, ", ")))
	}
	if approvalEffects(op.Effects) {
		if s.taskEffectsPreapproved {
			fmt.Fprintln(s.App.Err, "Task effects preapproved by insta exec --approve.")
		} else if e := s.App.Confirm("Run this operation with the displayed remote writes, model calls, or destructive effects?"); e != nil {
			return result, e
		}
	}
	cwd := op.Cwd
	if cwd == "" {
		cwd = t.Source
	}
	directory, e := manifest.Path(s.Root, cwd, true)
	if e != nil {
		return result, e
	}
	env := config.TaskEnvironment(s.Manifest, s.Values, s.Root, runner.Venv(s.Root, s.Project.SelectedStack))
	// Resolve Bedrock's supported credential chain instead of requiring static keys.
	if s.Values["LLM_PROVIDER"] == "bedrock" && contains(op.Effects, "remote-model-call") {
		code := "import boto3; c=boto3.Session().get_credentials(); assert c is not None, 'No AWS credentials found'; c.get_frozen_credentials(); print('AWS credential chain resolved')"
		if _, e := runner.Run(ctx, runner.Request{Argv: []string{runner.Python(s.Root, s.Project.SelectedStack), "-c", code}, Cwd: s.Root, Env: env, Output: s.App.Err, Redactor: s.Redactor}); e != nil {
			return result, failure("prerequisite", 3, errors.New("AWS credential chain could not resolve; configure an AWS profile, SSO, workload role, or environment credentials"))
		}
	}
	logRelative := filepath.Join(".insta", "logs", s.Project.SelectedStack+"-"+taskID+"-"+operation+".log")
	logPath := filepath.Join(s.Root, logRelative)
	if e = os.MkdirAll(filepath.Dir(logPath), 0700); e != nil {
		return result, e
	}
	log, e := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return result, e
	}
	defer log.Close()
	narrator := s.newTaskNarrator(taskID, operation, t, op)
	narrator.before(ctx)
	if e := ctx.Err(); e != nil {
		return result, failure("interrupted", 130, errors.New("task canceled before the operation started"))
	}
	result = state.Operation{Task: taskID, Name: operation, StartedAt: time.Now().UTC(), Status: "running", Verification: "Not independently verified.", Log: filepath.ToSlash(logRelative)}
	s.Project.Stack().Operations[taskID+"/"+operation] = result
	if e = s.Save(); e != nil {
		return result, e
	}
	output := s.App.Out
	if s.App.JSON {
		output = s.App.Err
	}
	fmt.Fprintln(s.App.Err, s.taskFeedback("Running "+taskID+"/"+operation+"..."))
	tail := &tailWriter{limit: 8192}
	runResult, runErr := runner.Run(ctx, runner.Request{Argv: runner.Args(s.Root, s.Project.SelectedStack, op.Command), Cwd: directory, Env: env, Stdin: s.App.In, Output: io.MultiWriter(output, log, tail), Redactor: s.Redactor, Interactive: op.Mode == "interactive"})
	finished := time.Now().UTC()
	result.FinishedAt = &finished
	result.ExitCode = &runResult.ExitCode
	result.OutputTail = tail.String()
	result.Status = "exited"
	if runErr != nil {
		result.Status = "failed"
	} else if op.Verification != "" {
		result.Verification = s.Redactor.Text(op.Verification)
	}
	s.Project.Stack().Operations[taskID+"/"+operation] = result
	if e = s.Save(); e != nil {
		return result, e
	}
	if result.OutputTail != "" && !strings.HasSuffix(result.OutputTail, "\n") {
		fmt.Fprintln(s.App.Err)
	}
	narrator.after(ctx, result, runResult.DurationMS)
	if ctx.Err() != nil {
		return result, &Error{Kind: "interrupted", Code: 130, ChildExitCode: result.ExitCode, Message: fmt.Sprintf("%s/%s interrupted; the recorded operation status is %s; inspect %s", taskID, operation, result.Status, logRelative)}
	}
	if runErr != nil {
		return result, &Error{Kind: "child_failed", Code: 6, ChildExitCode: &runResult.ExitCode, Message: fmt.Sprintf("%s/%s failed with child exit %d; inspect %s", taskID, operation, runResult.ExitCode, logRelative)}
	}
	return result, nil
}

func (s *session) checkIndex(ctx context.Context, index manifest.Index) error {
	name := s.Values[index.Config]
	if name == "" {
		name = index.Default
	}
	if name == "" {
		return fmt.Errorf("index setting %s is missing", index.Config)
	}
	if strings.ContainsAny(name, "/*?#, ") {
		return fmt.Errorf("index setting %s must identify one index", index.Config)
	}
	response, e := s.DataRequest(ctx, "GET", "/"+url.PathEscape(name)+"/_count", nil)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("index %s is unavailable (HTTP %d); run the task's ingest operation before query/validation", name, response.StatusCode)
	}
	var body struct {
		Count int64 `json:"count"`
	}
	if e = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body); e != nil {
		return errors.New("could not read the index document count")
	}
	if body.Count == 0 {
		return fmt.Errorf("index %s is empty; run the task's ingest operation", name)
	}
	return nil
}
func approvalEffects(effects []string) bool {
	for _, e := range effects {
		if strings.Contains(e, "write") || strings.Contains(e, "delete") || strings.Contains(e, "remote-model-call") || strings.Contains(e, "remote-api-call") {
			if e != "model-cache-write" && e != "local-cache-write" {
				return true
			}
		}
	}
	return false
}
func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
func serviceOrder(m *manifest.Manifest) []string {
	var out []string
	seen := map[string]bool{}
	var visit func(string)
	visit = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		for _, dep := range m.Services[n].Requires.Services {
			visit(dep)
		}
		out = append(out, n)
	}
	for _, name := range manifest.Keys(m.Services) {
		visit(name)
	}
	return out
}

type tailWriter struct {
	data  []byte
	limit int
}

func (w *tailWriter) Write(b []byte) (int, error) {
	w.data = append(w.data, b...)
	if len(w.data) > w.limit {
		w.data = w.data[len(w.data)-w.limit:]
	}
	return len(b), nil
}
func (w *tailWriter) String() string { return string(w.data) }
