package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/instaclustr/insta-cli/internal/manifest"
)

func TestBannerCoversHelpCommandsAndUsageFailures(t *testing.T) {
	cases := []struct {
		args []string
		ok   bool
	}{
		{nil, true},
		{[]string{"help"}, true},
		{[]string{"--help"}, true},
		{[]string{"help", "task"}, true},
		{[]string{"task", "run", "--help"}, true},
		{[]string{"lab", "list"}, true},
		{[]string{"version"}, true},
		{[]string{"unknown-command"}, false},
		{[]string{"--unknown-flag"}, false},
		{[]string{"task", "run"}, false},
	}
	for _, test := range cases {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			app := New(strings.NewReader(""), &out, &diagnostics)
			// main.go discovers signal behavior through Find before Execute.
			_, _, _ = app.Command().Find(test.args)
			code := app.Execute(context.Background(), test.args)
			if (code == 0) != test.ok {
				t.Fatalf("exit=%d: %s", code, diagnostics.String())
			}
			banner := "insta CLI v" + Version + "\n"
			if !strings.HasPrefix(diagnostics.String(), banner) || strings.Count(diagnostics.String(), banner) != 1 {
				t.Fatalf("banner absent, duplicated, or not first: %q", diagnostics.String())
			}
			if strings.Contains(out.String(), "insta CLI v") {
				t.Fatalf("banner polluted stdout: %q", out.String())
			}
		})
	}
}

func TestBannerDoesNotPolluteStructuredCatalogOrVersion(t *testing.T) {
	var out, diagnostics bytes.Buffer
	app := New(strings.NewReader(""), &out, &diagnostics)
	for _, args := range [][]string{{"lab", "list", "--json"}, {"version", "--json"}} {
		out.Reset()
		diagnostics.Reset()
		if code := app.Execute(context.Background(), args); code != 0 {
			t.Fatalf("exit=%d: %s", code, diagnostics.String())
		}
		decoder := json.NewDecoder(&out)
		var envelope struct {
			OK      bool            `json:"ok"`
			Command string          `json:"command"`
			Data    json.RawMessage `json:"data"`
		}
		if err := decoder.Decode(&envelope); err != nil || !envelope.OK {
			t.Fatalf("invalid JSON result: %v", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			t.Fatalf("trailing stdout content: %v", err)
		}
		if args[0] == "lab" {
			var labs []struct {
				ID, Name, Description string
			}
			if err := json.Unmarshal(envelope.Data, &labs); err != nil || len(labs) != 2 || labs[0].Description == "" || labs[1].Description == "" {
				t.Fatalf("catalog missing useful descriptions: %s", envelope.Data)
			}
		}
	}
}

func TestHelpAndCatalogOfferRunnableNextSteps(t *testing.T) {
	var out, diagnostics bytes.Buffer
	app := New(strings.NewReader(""), &out, &diagnostics)
	if code := app.Execute(context.Background(), []string{"--help"}); code != 0 {
		t.Fatal(diagnostics.String())
	}
	help := out.String()
	last := -1
	for _, command := range []string{"insta lab list", "insta new my-rag --lab rag-retrieval", "cd my-rag", "insta config", "insta up", "insta task list", "insta task start baseline-vector-rag", "insta task run baseline-vector-rag ingest", "insta exec", "insta down"} {
		index := strings.Index(help, command)
		if index <= last {
			t.Fatalf("help omits or misorders %q: %s", command, help)
		}
		last = index
	}
	out.Reset()
	if code := app.Execute(context.Background(), []string{"lab", "list"}); code != 0 {
		t.Fatal(diagnostics.String())
	}
	catalog := out.String()
	for _, expected := range []string{"rag-retrieval", "agentic-rag", "keyword", "MCP", "Create a project: insta new <working-copy> --lab <lab-id>"} {
		if !strings.Contains(catalog, expected) {
			t.Fatalf("catalog missing %q: %s", expected, catalog)
		}
	}
	if strings.Contains(catalog, "Welcome") || strings.Contains(catalog, "Would you like") {
		t.Fatal("catalog retained conversational filler")
	}
}

func TestTaskListingSupportsLegacyAndCustomMetadata(t *testing.T) {
	s := agentFixture(t)
	s.Manifest.Name = "RAG Retrieval"
	s.Manifest.Tasks = map[string]manifest.Task{
		"baseline-bm25-rag":   {Blueprint: "tasks/baseline-bm25-rag.md", Operations: map[string]manifest.Operation{"validate": {}, "query": {}, "ingest": {}}},
		"setup-environment":   {Blueprint: "tasks/setup-environment.md", Operations: map[string]manifest.Operation{"validate": {}}},
		"baseline-vector-rag": {Blueprint: "tasks/baseline-vector-rag.md", Operations: map[string]manifest.Operation{"ingest": {}}},
		"custom-task":         {Name: "Custom retrieval", Description: "Inspect the custom corpus.\x1b\nUse declared evidence.", Order: 1, Operations: map[string]manifest.Operation{"inspect": {}}},
		"other-task":          {Blueprint: "tasks/other.md", Operations: map[string]manifest.Operation{"run": {}}},
	}
	s.Project.Stack().ActiveTask = "baseline-vector-rag"
	text := formatTaskList(s)
	for _, expected := range []string{"Tasks: RAG Retrieval  |  stack dev", "baseline-vector-rag  |  Vector search baseline [active]", "Compare BM25 keyword matching", "Operations: ingest, query, validate", "Custom retrieval", "Inspect the custom corpus.", "see tasks/other.md", "insta task run <task-id> <operation>"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("listing missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "\x1b") {
		t.Fatal("metadata retained terminal control characters")
	}
	ids := orderedTaskIDs(s.Manifest)
	if strings.Join(ids, ",") != "custom-task,setup-environment,baseline-vector-rag,baseline-bm25-rag,other-task" {
		t.Fatalf("teaching/custom order lost: %v", ids)
	}
	custom := s.Manifest.Tasks["baseline-vector-rag"]
	custom.Name, custom.Description = "Renamed task", "User-defined description."
	if taskTitle(s.Manifest.ID, "baseline-vector-rag", custom) != custom.Name || taskDescription(s.Manifest.ID, "baseline-vector-rag", custom) != custom.Description {
		t.Fatal("built-in metadata overwrote customized project metadata")
	}
}

func TestLegacyTaskCatalogDescriptionsAndOrder(t *testing.T) {
	for _, test := range []struct {
		labID, orderedIDs, firstTitle, lastTitle string
	}{
		{
			labID:      "rag-retrieval",
			orderedIDs: "setup-environment,baseline-vector-rag,baseline-bm25-rag,hybrid-rag-native,hybrid-rag-ner-rerank,governance-tiering,operationalize-production",
			firstTitle: "Environment readiness",
			lastTitle:  "Operational policies and retention",
		},
		{
			labID:      "agentic-rag",
			orderedIDs: "introduction,news-agent,financials-agent,orchestrator-agent,client",
			firstTitle: "Architecture and readiness",
			lastTitle:  "Agent conversation",
		},
	} {
		t.Run(test.labID, func(t *testing.T) {
			m := &manifest.Manifest{ID: test.labID, Tasks: map[string]manifest.Task{}}
			for _, id := range strings.Split(test.orderedIDs, ",") {
				m.Tasks[id] = manifest.Task{Blueprint: "tasks/" + id + ".md"}
			}
			ids := orderedTaskIDs(m)
			if strings.Join(ids, ",") != test.orderedIDs {
				t.Fatalf("legacy teaching order lost: %v", ids)
			}
			if taskTitle(test.labID, ids[0], m.Tasks[ids[0]]) != test.firstTitle || taskTitle(test.labID, ids[len(ids)-1], m.Tasks[ids[len(ids)-1]]) != test.lastTitle {
				t.Fatal("legacy task titles lost")
			}
			for _, id := range ids {
				description := taskDescription(test.labID, id, m.Tasks[id])
				if len(description) < 40 || strings.HasPrefix(description, "Run the declared operations") {
					t.Fatalf("legacy task %s lacks a useful learning description: %q", id, description)
				}
			}
		})
	}
}
