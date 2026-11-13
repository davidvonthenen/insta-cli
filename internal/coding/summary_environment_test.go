package coding

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexProbeExcludesTaskSummaryEnvironment(t *testing.T) {
	for key, value := range map[string]string{
		"INSTA_SUMMARY_ENABLED": "true", "INSTA_SUMMARY_PROVIDER": "anthropic",
		"INSTA_SUMMARY_API_KEY": "summary-secret", "ANTHROPIC_API_KEY": "anthropic-secret",
		"ANTHROPIC_MODEL": "configured-model", "ANTHROPIC_BASE_URL": "https://example.com/v1",
		"INSTACLUSTR_PROVISION_API_KEY": "management-secret", "INSTA_ASSISTANT_API_KEY": "assistant-secret",
	} {
		t.Setenv(key, value)
	}
	c := &Codex{Executable: executableFixture(t, `
for name in INSTA_SUMMARY_ENABLED INSTA_SUMMARY_PROVIDER INSTA_SUMMARY_API_KEY ANTHROPIC_API_KEY ANTHROPIC_MODEL ANTHROPIC_BASE_URL INSTACLUSTR_PROVISION_API_KEY INSTA_ASSISTANT_API_KEY; do
  if printenv "$name" >/dev/null 2>&1; then exit 87; fi
done
if [ "$1" = '--version' ]; then printf 'codex-cli 0.164.0\n'; else printf 'Logged in\n'; fi
`)}
	readiness, err := c.Probe(context.Background())
	if err != nil || !readiness.Configured || readiness.State != Ready {
		t.Fatalf("diagnostic child received summary/task secrets: state=%s error=%v", readiness.State, err)
	}
}

func TestCodexTaskSummaryCredentialsStayInEnvironment(t *testing.T) {
	root := t.TempDir()
	c := &Codex{Executable: executableFixture(t, `
[ "$INSTA_SUMMARY_PROVIDER" = openai ] || exit 81
[ "$INSTA_SUMMARY_API_KEY" = summary-secret ] || exit 82
[ "${ANTHROPIC_API_KEY-unset}" = unset ] || exit 83
[ "${INSTACLUSTR_PROVISION_API_KEY-unset}" = unset ] || exit 84
[ "${INSTA_ASSISTANT_API_KEY-unset}" = unset ] || exit 85
printf '%s\n' "$@" > args
cat > prompt
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"environment-ok"}}' '{"type":"turn.completed"}'
`)}
	var out bytes.Buffer
	req := SessionRequest{
		Workspace: root, OneShot: true, Instruction: "Explain the selected task", Stdout: &out,
		Environment: []string{
			"PATH=/usr/bin:/bin", "INSTA_SUMMARY_PROVIDER=openai", "INSTA_SUMMARY_API_KEY=summary-secret",
			"INSTACLUSTR_PROVISION_API_KEY=management-secret", "INSTA_ASSISTANT_API_KEY=assistant-secret",
		},
	}
	if _, err := c.RunPrompt(context.Background(), req, ""); err != nil || out.String() != "environment-ok\n" {
		t.Fatalf("summary settings did not reach coding session environment: %v", err)
	}
	for _, name := range []string{"args", "prompt"} {
		contents, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"summary-secret", "anthropic-secret", "management-secret", "assistant-secret"} {
			if strings.Contains(string(contents), secret) {
				t.Errorf("credential appeared in %s", name)
			}
		}
		if strings.Contains(string(contents), "shell_environment_policy.set.INSTA_SUMMARY") {
			t.Errorf("summary credentials were forced through %s", name)
		}
	}
}
