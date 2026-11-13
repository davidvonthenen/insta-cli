package config

import (
	"strings"
	"testing"

	"github.com/instaclustr/insta-cli/internal/manifest"
	"github.com/instaclustr/insta-cli/internal/state"
)

func TestSummarySettingsResolveAndProtectSecrets(t *testing.T) {
	m := &manifest.Manifest{Config: map[string]manifest.Definition{}}
	p, _ := state.New("rag-retrieval", nil)
	p.Stack().Config["summary.provider"] = "anthropic"
	p.Stack().Config["summary.model"] = "stored-model"
	env := map[string]string{"INSTA_SUMMARY_MODEL": "shell-model", "INSTA_SUMMARY_API_KEY": "shell-summary-key", "ANTHROPIC_API_KEY": "shell-anthropic-key"}
	values, view, err := Resolve(m, p, map[string]string{"summary.api_key": "stored-summary-key"}, func(key string) (string, bool) { value, ok := env[key]; return value, ok })
	if err != nil {
		t.Fatal(err)
	}
	if values["summary.enabled"] != "true" || values["summary.timeout_seconds"] != "20" || values["summary.provider"] != "anthropic" || values["summary.model"] != "shell-model" {
		t.Fatal("summary defaults or precedence lost")
	}
	for _, key := range []string{"summary.api_key", "ANTHROPIC_API_KEY"} {
		if view[key].Value != "[redacted]" || !view[key].Secret {
			t.Errorf("%s not protected", key)
		}
	}
	secrets := strings.Join(SecretValues(m, values), ",")
	if !strings.Contains(secrets, "shell-summary-key") || !strings.Contains(secrets, "shell-anthropic-key") {
		t.Fatal("summary credentials not included in redaction")
	}
	for _, timeout := range []string{"0", "61", "-1", "NaN"} {
		if Validate("summary.timeout_seconds", timeout, Definitions(m)["summary.timeout_seconds"]) == nil {
			t.Errorf("invalid timeout %q accepted", timeout)
		}
	}
}

func TestAnthropicSummarySettingsAreReservedPlaceholders(t *testing.T) {
	m := &manifest.Manifest{Config: map[string]manifest.Definition{}}
	defs := Definitions(m)
	if !strings.Contains(defs["summary.provider"].Description, "placeholders") {
		t.Fatal("provider setting does not explain the implementation boundary")
	}
	for _, provider := range []string{"anthropic", "claude"} {
		if err := Validate("summary.provider", provider, defs["summary.provider"]); err != nil {
			t.Fatal("placeholder is not selectable", err)
		}
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL"} {
		if defs[key].Default != "" || !strings.Contains(defs[key].Description, "not implemented") {
			t.Errorf("%s advertises an implemented Claude summary setting or has an active default", key)
		}
	}
	m.Config["ANTHROPIC_MODEL"] = manifest.Definition{Default: "lab-application-model", Description: "Lab application model"}
	if def := Definitions(m)["ANTHROPIC_MODEL"]; def.Default != "lab-application-model" || def.Description != "Lab application model" {
		t.Fatal("reserved summary settings replaced a declared application setting")
	}
}

func TestSummaryCredentialsAreExcludedFromTaskEnvironment(t *testing.T) {
	t.Setenv("INSTA_SUMMARY_API_KEY", "summary-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("ANTHROPIC_MODEL", "model")
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com/v1")
	m := &manifest.Manifest{Config: map[string]manifest.Definition{}}
	for _, entry := range TaskEnvironment(m, map[string]string{"summary.api_key": "stored-key"}, "/project", "/venv") {
		if strings.HasPrefix(entry, "INSTA_SUMMARY_") || strings.HasPrefix(entry, "ANTHROPIC_") {
			t.Errorf("summary-only setting forwarded: %s", strings.SplitN(entry, "=", 2)[0])
		}
	}
	m.Config["ANTHROPIC_API_KEY"] = manifest.Definition{Secret: true}
	entries := strings.Join(TaskEnvironment(m, map[string]string{"ANTHROPIC_API_KEY": "declared-key"}, "/project", "/venv"), "\n")
	if !strings.Contains(entries, "ANTHROPIC_API_KEY=declared-key") {
		t.Fatal("declared lab Anthropic credential was removed")
	}
}
