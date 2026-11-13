package narration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestConfiguredProviderSelectionAndCredentialBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		values   map[string]string
		provider string
		model    string
		key      string
		endpoint string
		err      bool
		disabled bool
	}{
		{name: "openai lab", values: map[string]string{"LLM_PROVIDER": "openai", "OPENAI_API_KEY": "openai-key", "OPENAI_MODEL": "configured-model"}, provider: "openai", model: "configured-model", key: "openai-key", endpoint: DefaultOpenAIBaseURL + "/chat/completions"},
		{name: "agentic lab without provider", values: map[string]string{"OPENAI_API_KEY": "openai-key", "OPENAI_MODEL": "configured-model"}, provider: "openai", model: "configured-model", key: "openai-key"},
		{name: "OpenAI preferred when both keys exist", values: map[string]string{"OPENAI_API_KEY": "openai-key", "OPENAI_MODEL": "configured-model", "ANTHROPIC_API_KEY": "anthropic-key"}, provider: "openai", model: "configured-model", key: "openai-key"},
		{name: "dedicated overrides", values: map[string]string{"summary.provider": "openai", "summary.api_key": "summary-key", "summary.model": "summary-model", "summary.base_url": "https://summary.example/v1", "OPENAI_API_KEY": "lab-key", "OPENAI_MODEL": "lab-model"}, provider: "openai", model: "summary-model", key: "summary-key", endpoint: "https://summary.example/v1/chat/completions"},
		{name: "compatible lab endpoint retained", values: map[string]string{"OPENAI_API_KEY": "lab-key", "OPENAI_MODEL": "lab-model", "OPENAI_BASE_URL": "https://lab.example/openai/v1"}, provider: "openai", model: "lab-model", key: "lab-key", endpoint: "https://lab.example/openai/v1/chat/completions"},
		{name: "same endpoint normalized", values: map[string]string{"OPENAI_API_KEY": "key", "OPENAI_MODEL": "model", "summary.base_url": "https://api.openai.com/v1/chat/completions"}, provider: "openai", model: "model", key: "key"},
		{name: "disabled", values: map[string]string{"summary.enabled": "false"}, disabled: true},
		{name: "missing configuration", values: map[string]string{}, err: true},
		{name: "missing model", values: map[string]string{"OPENAI_API_KEY": "key"}, err: true},
		{name: "no cross-provider credentials", values: map[string]string{"summary.provider": "anthropic", "OPENAI_API_KEY": "key", "OPENAI_MODEL": "model"}, err: true},
		{name: "no changed destination credential reuse", values: map[string]string{"summary.base_url": "https://other.example/v1", "OPENAI_API_KEY": "key", "OPENAI_MODEL": "model"}, err: true},
		{name: "bedrock does not use dormant OpenAI", values: map[string]string{"LLM_PROVIDER": "bedrock", "OPENAI_API_KEY": "key", "OPENAI_MODEL": "model", "ANTHROPIC_API_KEY": "other-key"}, err: true},
		{name: "explicit summary overrides bedrock", values: map[string]string{"LLM_PROVIDER": "bedrock", "summary.provider": "openai", "OPENAI_API_KEY": "key", "OPENAI_MODEL": "model"}, provider: "openai", model: "model", key: "key"},
		{name: "unsupported", values: map[string]string{"summary.provider": "unsupported"}, err: true},
		{name: "bad timeout", values: map[string]string{"summary.timeout_seconds": "61"}, err: true},
		{name: "invalid enabled", values: map[string]string{"summary.enabled": "yes"}, err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ConfigFromValues(tc.values)
			if (err != nil) != tc.err {
				t.Fatalf("config error = %v; want error %t", err, tc.err)
			}
			if err != nil {
				return
			}
			if cfg.Enabled == tc.disabled || cfg.Provider != tc.provider || cfg.Model != tc.model || cfg.APIKey != tc.key || (tc.endpoint != "" && cfg.Endpoint != tc.endpoint) {
				t.Fatalf("wrong configuration: enabled=%t provider=%q model=%q endpoint=%q", cfg.Enabled, cfg.Provider, cfg.Model, cfg.Endpoint)
			}
		})
	}
}

func TestAPIPayloadAuthenticationAndRedaction(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong request method or content type")
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "api-secret") || strings.Contains(string(body), "lab-secret") {
			t.Error("request body disclosed known secrets")
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
		}
		if payload["model"] != "configured-model" || payload["tools"] != nil || payload["stream"] != nil {
			t.Error("wrong model, unexpected tools or streaming")
		}
		messages := payload["messages"].([]any)
		user := messages[len(messages)-1].(map[string]any)["content"].(string)
		if !strings.Contains(user, "Summary phase: after") || !strings.Contains(user, "[redacted]") || !strings.Contains(user, "untrusted fixture output") {
			t.Errorf("missing phase or evidence: %s", user)
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer api-secret" || r.Header.Get("x-api-key") != "" || payload["store"] != false || payload["max_completion_tokens"] != float64(2048) || len(messages) != 2 {
			t.Error("OpenAI request contract mismatch")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "Observed outcome: api-secret, lab-secret.\n\nWhy it matters: retrieval evidence.\x1b\x07"}}}})
	}))
	defer server.Close()
	client := Client{Config: Config{Enabled: true, Provider: "openai", Endpoint: server.URL, Model: "configured-model", APIKey: "api-secret"}}
	result, err := client.Summarize(context.Background(), "after", "untrusted fixture output: api-secret, lab-secret", func(s string) string { return strings.ReplaceAll(s, "lab-secret", "[redacted]") })
	if err != nil || calls.Load() != 1 {
		t.Fatalf("calls=%d error=%v", calls.Load(), err)
	}
	if strings.Contains(result, "secret") || strings.ContainsAny(result, "\x1b\x07\r\n") || !strings.Contains(result, "[redacted]") || !strings.Contains(result, "Why it matters") {
		t.Fatalf("unsafe or incomplete output: %s", result)
	}
}

func TestAnthropicPlaceholderNeverMakesRequestsOrChangesProvider(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	for name, values := range map[string]map[string]string{
		"explicit provider":     {"summary.provider": "anthropic"},
		"explicit Claude alias": {"summary.provider": "claude"},
		"lab provider":          {"LLM_PROVIDER": "anthropic", "OPENAI_API_KEY": "openai-key", "OPENAI_MODEL": "other-model"},
		"lab Claude alias":      {"LLM_PROVIDER": "claude", "OPENAI_API_KEY": "openai-key", "OPENAI_MODEL": "other-model"},
		"only Anthropic key":    {"ANTHROPIC_API_KEY": "anthropic-key"},
	} {
		t.Run(name, func(t *testing.T) {
			values["summary.api_key"] = "summary-key"
			values["summary.model"] = "reserved-model"
			values["summary.base_url"] = server.URL
			cfg, err := ConfigFromValues(values)
			var placeholder *NotImplementedError
			if !errors.As(err, &placeholder) || placeholder.Provider != "anthropic" || cfg.Provider != "anthropic" || !strings.Contains(err.Error(), "placeholder") {
				t.Fatalf("provider selection failed to report its placeholder: provider=%q err=%v", cfg.Provider, err)
			}
			if cfg.APIKey != "" || cfg.Model != "" || cfg.Endpoint != "" {
				t.Fatal("placeholder prepared a credential, model, or endpoint")
			}
		})
	}
	// The request boundary itself must remain protected if a caller constructs
	// Config directly rather than using ConfigFromValues.
	for _, provider := range []string{"anthropic", "claude"} {
		client := Client{Config: Config{Enabled: true, Provider: provider, Endpoint: server.URL, Model: "reserved-model", APIKey: "private-key"}}
		for _, phase := range []string{"before", "after"} {
			text, err := client.Summarize(context.Background(), phase, "evidence", nil)
			var placeholder *NotImplementedError
			if text != "" || !errors.As(err, &placeholder) || placeholder.Provider != "anthropic" {
				t.Errorf("direct %s %s request did not report placeholder: %q / %v", provider, phase, text, err)
			}
		}
		client.Config.Enabled = false
		if text, err := client.Summarize(context.Background(), "before", "evidence", nil); text != "" || err != nil {
			t.Fatal("disabled summary did not bypass its provider", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("placeholder sent an HTTP request")
	}
}

func TestCancellationTimeoutAndNoRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	client := Client{Config: Config{Enabled: true, Provider: "openai", Endpoint: server.URL, Model: "model", APIKey: "key", Timeout: 25 * time.Millisecond}}
	_, err := client.Summarize(context.Background(), "before", "evidence", nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") || calls.Load() != 1 {
		t.Fatalf("timeout requests=%d err=%v", calls.Load(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Summarize(ctx, "before", "evidence", nil)
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("cancelled requests=%d err=%v", calls.Load(), err)
	}
	client.Config.Enabled = false
	if value, err := client.Summarize(context.Background(), "before", "evidence", nil); err != nil || value != "" || calls.Load() != 1 {
		t.Fatal("disabled summaries made a request")
	}
}

func TestRedirectCannotForwardCredentials(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	httpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { t.Error("caller's redirect policy was used"); return nil }}
	client := Client{HTTP: httpClient, Config: Config{Enabled: true, Provider: "openai", Endpoint: origin.URL, Model: "model", APIKey: "private-key"}}
	if _, err := client.Summarize(context.Background(), "before", "evidence", nil); err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatal("redirect was not reported", err)
	}
	if targetCalls.Load() != 0 {
		t.Fatal("credentials reached redirect target")
	}
}

func TestSafeErrorsAndResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name, provider, body string
		status               int
	}{
		{"server error", "openai", `{"error":"secret; https://private.example; malicious data"}`, 500},
		{"invalid JSON", "openai", `not JSON secret`, 200},
		{"truncated completion", "openai", `{"choices":[{"finish_reason":"length","message":{"content":"false success"}}]}`, 200},
		{"no content", "openai", `{"choices":[{"finish_reason":"stop","message":{"content":""}}]}`, 200},
		{"tool call", "openai", `{"choices":[{"finish_reason":"stop","message":{"content":"claim","tool_calls":[{"function":{"name":"run"}}]}}]}`, 200},
		{"oversized response", "openai", strings.Repeat("x", maxResponseBytes+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			client := Client{Config: Config{Enabled: true, Provider: tc.provider, Endpoint: server.URL, Model: "model", APIKey: "secret"}}
			value, err := client.Summarize(context.Background(), "after", "evidence", nil)
			if err == nil || value != "" || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private.example") {
				t.Fatalf("unsafe response accepted: value=%q err=%v", value, err)
			}
		})
	}
}

func TestBoundedContextAndOutputPreserveUTF8(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		content := payload.Messages[1].Content
		if len(content) > MaxEvidenceBytes+100 || !utf8.ValidString(content) || !strings.HasSuffix(content, "[truncated]") {
			t.Error("context was not bounded with a truncation marker")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": strings.Repeat("観測結果 ", 1000)}}}})
	}))
	defer server.Close()
	client := Client{Config: Config{Enabled: true, Provider: "openai", Endpoint: server.URL, Model: "model", APIKey: "key"}}
	result, err := client.Summarize(context.Background(), "after", strings.Repeat("観測結果", MaxEvidenceBytes), nil)
	if err != nil || !utf8.ValidString(result) || len(result) > maxSummaryBytes || len(strings.Fields(result)) > maxSummaryWords+1 {
		t.Fatalf("unbounded or invalid summary: bytes=%d err=%v", len(result), err)
	}
}

func TestEndpointValidation(t *testing.T) {
	for _, base := range []string{"http://example.com/v1", "https://user:password@example.com/v1", "https://example.com/v1?key=secret", "https://example.com/v1#fragment", "file:///tmp/model", "https://example.com/\nprivate", "relative/path"} {
		if _, err := endpoint(base); err == nil {
			t.Errorf("unsafe URL accepted: %q", base)
		}
	}
	for base, want := range map[string]string{"https://example.com": "https://example.com/v1/chat/completions", "https://example.com/v1/": "https://example.com/v1/chat/completions", "https://example.com/prefix/v1/chat/completions": "https://example.com/prefix/v1/chat/completions", "http://127.0.0.1:1234": "http://127.0.0.1:1234/v1/chat/completions", "http://[::1]:1234/v1": "http://[::1]:1234/v1/chat/completions"} {
		if got, err := endpoint(base); err != nil || got != want {
			t.Errorf("endpoint(%q) = %q, %v; want %q", base, got, err, want)
		}
	}
}
