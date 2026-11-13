package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/instaclustr/insta-cli/internal/manifest"
	"github.com/instaclustr/insta-cli/internal/narration"
	"github.com/instaclustr/insta-cli/internal/state"
)

func environmentMap(entries []string) map[string]string {
	values := map[string]string{}
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}

func TestCodingEnvironmentPreservesNestedSummarySelection(t *testing.T) {
	t.Setenv("INSTACLUSTR_PROVISION_API_KEY", "management-secret")
	t.Setenv("INSTACLUSTR_PROVISION_USERNAME", "management-user")
	t.Setenv("INSTA_ASSISTANT_API_KEY", "assistant-secret")
	t.Setenv("INSTA_SUMMARY_PROVIDER", "stale-provider")
	t.Setenv("INSTA_SUMMARY_MODEL", "stale-model")
	t.Setenv("INSTA_SUMMARY_API_KEY", "stale-summary-secret")
	t.Setenv("ANTHROPIC_API_KEY", "stale-anthropic-secret")
	for _, tc := range []struct {
		name  string
		shell map[string]string
	}{
		{"explicit OpenAI", map[string]string{
			"INSTA_SUMMARY_PROVIDER": "openai", "INSTA_SUMMARY_MODEL": "shell-model",
			"INSTA_SUMMARY_API_KEY": "shell-summary-secret", "INSTA_SUMMARY_BASE_URL": "https://summaries.example/v1",
			"INSTA_SUMMARY_TIMEOUT_SECONDS": "7",
		}},
		{"empty overrides", map[string]string{
			"INSTA_SUMMARY_PROVIDER": "openai", "INSTA_SUMMARY_MODEL": "", "INSTA_SUMMARY_API_KEY": "",
		}},
		{"disabled", map[string]string{"INSTA_SUMMARY_ENABLED": "false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &manifest.Manifest{Config: map[string]manifest.Definition{
				"LLM_PROVIDER": {Default: "openai"}, "OPENAI_MODEL": {Default: "lab-model"},
				"OPENAI_API_KEY": {Secret: true},
			}}
			p, err := state.New("rag-retrieval", nil)
			if err != nil {
				t.Fatal(err)
			}
			p.Stack().Config["summary.provider"] = "openai"
			p.Stack().Config["summary.model"] = "stored-model"
			p.Stack().Config["summary.timeout_seconds"] = "55"
			secrets := map[string]string{"summary.api_key": "stored-summary-secret", "OPENAI_API_KEY": "stored-openai-secret"}
			parent, _, err := Resolve(m, p, secrets, func(key string) (string, bool) { v, ok := tc.shell[key]; return v, ok })
			if err != nil {
				t.Fatal(err)
			}
			env := environmentMap(CodingEnvironment(m, parent, "/project", "/venv"))
			for _, key := range []string{"INSTACLUSTR_PROVISION_USERNAME", "INSTACLUSTR_PROVISION_API_KEY", "INSTA_ASSISTANT_API_KEY"} {
				if _, exists := env[key]; exists {
					t.Errorf("coding session retained excluded setting %s", key)
				}
			}
			for key, value := range tc.shell {
				if actual, exists := env[key]; !exists || actual != value {
					t.Errorf("coding session lost shell override for %s", key)
				}
			}
			nested, _, err := Resolve(m, p, secrets, func(key string) (string, bool) { v, ok := env[key]; return v, ok })
			if err != nil {
				t.Fatal(err)
			}
			want, err := narration.ConfigFromValues(parent)
			if err != nil {
				t.Fatal(err)
			}
			got, err := narration.ConfigFromValues(nested)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("nested task changed effective summary configuration: %v", err)
			}
		})
	}
}

func TestCodingEnvironmentPreservesClaudePlaceholderWithoutCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, provider, labProvider, openAIKey string
	}{
		{"explicit Anthropic", "anthropic", "openai", "lab-openai-secret"},
		{"explicit Claude alias", "claude", "openai", "lab-openai-secret"},
		{"automatic lab provider", "auto", "anthropic", "lab-openai-secret"},
		{"automatic lab alias", "auto", "claude", "lab-openai-secret"},
		{"automatic Anthropic credential", "auto", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &manifest.Manifest{Config: map[string]manifest.Definition{
				"LLM_PROVIDER": {}, "OPENAI_API_KEY": {Secret: true}, "OPENAI_MODEL": {},
			}}
			p, err := state.New("rag-retrieval", nil)
			if err != nil {
				t.Fatal(err)
			}
			shell := map[string]string{
				"INSTA_SUMMARY_ENABLED": "true", "INSTA_SUMMARY_PROVIDER": tc.provider,
				"INSTA_SUMMARY_API_KEY": "unsupported-summary-secret", "INSTA_SUMMARY_MODEL": "reserved-model",
				"LLM_PROVIDER": tc.labProvider, "OPENAI_API_KEY": tc.openAIKey, "OPENAI_MODEL": "lab-model",
				"ANTHROPIC_API_KEY": "reserved-anthropic-secret", "ANTHROPIC_MODEL": "reserved-provider-model",
				"ANTHROPIC_BASE_URL": "https://claude.example/v1",
			}
			parent, _, err := Resolve(m, p, nil, func(key string) (string, bool) { v, ok := shell[key]; return v, ok })
			if err != nil {
				t.Fatal(err)
			}
			env := environmentMap(CodingEnvironment(m, parent, "/project", "/venv"))
			for _, key := range []string{"INSTA_SUMMARY_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL"} {
				if value, exists := env[key]; !exists || value != "" {
					t.Errorf("reserved provider setting reached coding session: %s", key)
				}
			}
			nested, _, err := Resolve(m, p, nil, func(key string) (string, bool) { v, ok := env[key]; return v, ok })
			if err != nil {
				t.Fatal(err)
			}
			for _, values := range []map[string]string{parent, nested} {
				cfg, err := narration.ConfigFromValues(values)
				var placeholder *narration.NotImplementedError
				if !errors.As(err, &placeholder) || cfg.Provider != "anthropic" || placeholder.Provider != "anthropic" {
					t.Fatalf("placeholder selection changed or fell back to OpenAI: provider=%q err=%v", cfg.Provider, err)
				}
			}
		})
	}
}

func TestCodingEnvironmentOnlyForwardsDeclaredAnthropicApplicationSettings(t *testing.T) {
	m := &manifest.Manifest{Config: map[string]manifest.Definition{}}
	values := map[string]string{"summary.enabled": "false", "summary.provider": "anthropic", "summary.api_key": "summary-secret", "ANTHROPIC_API_KEY": "anthropic-secret", "ANTHROPIC_MODEL": "application-model", "ANTHROPIC_BASE_URL": "https://application.example/v1"}
	check := func(declared bool, enabled string) {
		t.Helper()
		values["summary.enabled"] = enabled
		env := environmentMap(CodingEnvironment(m, values, "/project", "/venv"))
		if env["INSTA_SUMMARY_ENABLED"] != enabled || env["INSTA_SUMMARY_PROVIDER"] != "anthropic" {
			t.Fatal("summary controls were lost")
		}
		if key, exists := env["INSTA_SUMMARY_API_KEY"]; !exists || key != "" {
			t.Fatal("disabled summary credential was forwarded or stored-key clearing was lost")
		}
		for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL"} {
			want := ""
			if declared {
				want = values[key]
			}
			if value, exists := env[key]; !exists || value != want {
				t.Errorf("Anthropic setting did not respect the declared lab contract: %s", key)
			}
		}
	}
	check(false, "false")
	check(false, "true")
	m.Config["ANTHROPIC_API_KEY"] = manifest.Definition{Secret: true}
	m.Config["ANTHROPIC_MODEL"] = manifest.Definition{}
	m.Config["ANTHROPIC_BASE_URL"] = manifest.Definition{}
	check(true, "false")
	check(true, "true")
}
