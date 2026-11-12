package cli

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/instaclustr/insta-cli/internal/bootstrap"
	"github.com/instaclustr/insta-cli/internal/manifest"
)

// Task metadata is optional so projects created with an earlier CLI retain a
// useful catalog without rewriting their approved executable manifests.
type taskDisplay struct {
	name, description string
	order             int
}

var taskDisplays = map[string]map[string]taskDisplay{
	"rag-retrieval": {
		"setup-environment":         {"Environment readiness", "Check the configured environment and dependencies before running retrieval exercises.", 1},
		"baseline-vector-rag":       {"Vector search baseline", "Index recipe embeddings and inspect semantic retrieval and generated answers as a baseline for later comparisons.", 2},
		"baseline-bm25-rag":         {"Keyword search baseline", "Compare BM25 keyword matching with the vector baseline and inspect how named-entity boosts affect ranking.", 3},
		"hybrid-rag-native":         {"Native hybrid search", "Combine keyword and vector retrieval with an OpenSearch search pipeline, then inspect the merged evidence.", 4},
		"hybrid-rag-ner-rerank":     {"Filters and vector refinement", "Ground results with BM25 and explicit metadata filters, then refine eligible candidates with vector search.", 5},
		"governance-tiering":        {"Governed retrieval and audit", "Apply document tier labels and retrieval policies, then inspect evidence boundaries and audit records.", 6},
		"operationalize-production": {"Operational policies and retention", "Inspect policy history and operational checks, including audit retention that can schedule index deletion.", 7},
	},
	"agentic-rag": {
		"introduction":       {"Architecture and readiness", "Explore the specialist-agent architecture and check Python syntax before running live requests.", 1},
		"news-agent":         {"News specialist", "Combine historical news retrieval with current reporting through MCP, then inspect citations and release decisions.", 2},
		"financials-agent":   {"Financial specialist", "Combine company filings with market data through MCP; inspect issuer identity, timestamps, and audit evidence.", 3},
		"orchestrator-agent": {"Specialist orchestration", "Start the orchestrator and its specialist dependencies, then inspect the audit trail after client requests.", 4},
		"client":             {"Agent conversation", "Send requests through the interactive client and inspect specialist routing, answers, and supporting evidence.", 5},
	},
}

func taskTitle(labID, taskID string, task manifest.Task) string {
	if strings.TrimSpace(task.Name) != "" {
		return displayText(task.Name)
	}
	if known, ok := taskDisplays[labID][taskID]; ok {
		return known.name
	}
	return displayText(strings.ReplaceAll(taskID, "-", " "))
}

func taskDescription(labID, taskID string, task manifest.Task) string {
	if strings.TrimSpace(task.Description) != "" {
		return displayText(task.Description)
	}
	if known, ok := taskDisplays[labID][taskID]; ok {
		return known.description
	}
	return "Run the declared operations for this exercise; see " + displayText(task.Blueprint) + " for its learning goals."
}

func taskOrder(labID, taskID string, task manifest.Task) int {
	if task.Order > 0 {
		return task.Order
	}
	if known, ok := taskDisplays[labID][taskID]; ok {
		return known.order
	}
	return int(^uint(0) >> 1)
}

func orderedTaskIDs(m *manifest.Manifest) []string {
	ids := manifest.Keys(m.Tasks)
	sort.SliceStable(ids, func(i, j int) bool {
		left, right := taskOrder(m.ID, ids[i], m.Tasks[ids[i]]), taskOrder(m.ID, ids[j], m.Tasks[ids[j]])
		if left == right {
			return ids[i] < ids[j]
		}
		return left < right
	})
	return ids
}

func operationNames(task manifest.Task) []string {
	order := map[string]int{"ingest": 1, "query": 2, "policy-registry": 3, "validate": 4, "audit": 5}
	names := manifest.Keys(task.Operations)
	sort.SliceStable(names, func(i, j int) bool {
		a, b := order[names[i]], order[names[j]]
		if a == 0 {
			a = 100
		}
		if b == 0 {
			b = 100
		}
		if a == b {
			return names[i] < names[j]
		}
		return a < b
	})
	return names
}

func formatLabList(labs []bootstrap.Lab) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Available labs (%d)\n", len(labs))
	for _, lab := range labs {
		fmt.Fprintf(&text, "\n%s  |  %s\n", displayText(lab.ID), displayText(lab.Name))
		text.WriteString(wrapDescription(lab.Description, "  ", 84))
		text.WriteByte('\n')
	}
	text.WriteString("\nCreate a project: insta new <working-copy> --lab <lab-id>\n")
	text.WriteString("Then: cd <working-copy>, insta config, insta up")
	return text.String()
}

func formatTaskList(s *session) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Tasks: %s  |  stack %s\n", displayText(s.Manifest.Name), displayText(s.Project.SelectedStack))
	for _, id := range orderedTaskIDs(s.Manifest) {
		task := s.Manifest.Tasks[id]
		active := ""
		if id == s.Project.Stack().ActiveTask {
			active = " [active]"
		}
		fmt.Fprintf(&text, "\n%s  |  %s%s\n", id, taskTitle(s.Manifest.ID, id, task), active)
		text.WriteString(wrapDescription(taskDescription(s.Manifest.ID, id, task), "  ", 84))
		fmt.Fprintf(&text, "\n  Operations: %s\n", strings.Join(operationNames(task), ", "))
	}
	text.WriteString("\nPrepare services: insta task start <task-id>\n")
	text.WriteString("Run an operation: insta task run <task-id> <operation>\n")
	text.WriteString("Review progress:  insta task status <task-id>")
	return text.String()
}

func displayText(value string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)), " ")
}

func wrapDescription(value, indent string, width int) string {
	words := strings.Fields(displayText(value))
	var lines []string
	line := indent
	for _, word := range words {
		if len(line) > len(indent) && len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = indent
		}
		if len(line) > len(indent) {
			line += " "
		}
		line += word
	}
	if len(line) > len(indent) {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
