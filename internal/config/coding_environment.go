package config

import (
	"strings"

	"github.com/instaclustr/insta-cli/internal/manifest"
)

// CodingEnvironment extends the lab runtime environment for a coding session
// that can invoke nested insta task commands. Effective summary settings travel
// in the environment, never in prompts or process arguments. Codex's configured
// shell environment policy still controls what its shell tools inherit.
//
// Unlike Python task processes, nested insta commands need the summary provider
// configuration and, for enabled OpenAI summaries, its credentials. Reserved
// Claude summary credentials are excluded. Explicit empty settings are retained
// so a shell override cannot fall back to a different stored value.
func CodingEnvironment(m *manifest.Manifest, values map[string]string, root, venv string) []string {
	env := map[string]string{}
	for _, entry := range TaskEnvironment(m, values, root, venv) {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	enabled := values["summary.enabled"] != "false"
	provider := codingSummaryProvider(values)
	for key, def := range Definitions(m) {
		if !strings.HasPrefix(key, "summary.") {
			continue
		}
		value := values[key]
		if (!enabled || provider != "openai") && def.Secret {
			value = ""
		}
		env[def.Env] = value
	}
	if enabled && provider == "anthropic" {
		// Auto selection may depend on the reserved Anthropic key. Preserve the
		// placeholder choice before removing that key from the child environment.
		// A nested CLI must not switch providers after credentials are filtered.
		configured := strings.ToLower(strings.TrimSpace(values["summary.provider"]))
		if configured == "" || configured == "auto" {
			env["INSTA_SUMMARY_PROVIDER"] = "anthropic"
		}
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL"} {
		value := values[key]
		if _, declared := m.Config[key]; !declared {
			value = ""
		}
		env[key] = value
	}
	entries := make([]string, 0, len(env))
	for _, key := range manifest.Keys(env) {
		entries = append(entries, key+"="+env[key])
	}
	return entries
}

// This credential-routing decision mirrors summary provider selection without
// constructing a client or exposing credentials to an unsupported provider.
// Configuration/narration regression tests check both sides together.
func codingSummaryProvider(values map[string]string) string {
	provider := strings.ToLower(strings.TrimSpace(values["summary.provider"]))
	if provider == "" || provider == "auto" {
		provider = strings.ToLower(strings.TrimSpace(values["LLM_PROVIDER"]))
		if provider == "" {
			switch {
			case strings.TrimSpace(values["OPENAI_API_KEY"]) != "":
				provider = "openai"
			case strings.TrimSpace(values["ANTHROPIC_API_KEY"]) != "":
				provider = "anthropic"
			}
		}
	}
	if provider == "claude" {
		return "anthropic"
	}
	return provider
}
