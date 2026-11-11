package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"AGENTS.md", "requirements.txt", "task.md", "code/entry.py"} {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0755)
		os.WriteFile(full, []byte("fixture\n"), 0644)
	}
	text := `schema_version: 1
id: fixture
name: Fixture
instructions: AGENTS.md
runtime: {kind: python, python: "3.12", requirements: requirements.txt}
config: {KEY: {secret: true}, MODE: {default: first}}
platforms: {}
tasks:
  example:
    blueprint: task.md
    source: code
    environment:
      conditional:
        - {operations: [run], when: {MODE: first}, required: [KEY]}
    operations:
      run: {command: [python, entry.py]}
lifecycle: {up: [], down: []}
`
	os.WriteFile(filepath.Join(root, "insta.yaml"), []byte(text), 0644)
	return root
}
func TestStrictManifestAndPathConfinement(t *testing.T) {
	root := fixture(t)
	m, hash, e := Load(root)
	if e != nil || len(hash) != 64 || m.ID != "fixture" {
		t.Fatal(m, hash, e)
	}
	file := filepath.Join(root, "insta.yaml")
	original, _ := os.ReadFile(file)
	for _, bad := range []string{string(original) + "unknown_field: bad\n", string(original) + "---\n{}\n", strings.Replace(string(original), "operations: [run]", "operations: [missing]", 1), strings.Replace(string(original), "required: [KEY]", "required: [MISSING]", 1), strings.Replace(string(original), "secret: true", "secret: true, default: leaked", 1)} {
		os.WriteFile(file, []byte(bad), 0644)
		if _, _, e := Load(root); e == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
	os.WriteFile(file, original, 0644)
	outside := filepath.Join(t.TempDir(), "outside.md")
	os.WriteFile(outside, []byte("private"), 0600)
	os.Remove(filepath.Join(root, "task.md"))
	os.Symlink(outside, filepath.Join(root, "task.md"))
	if _, _, e := Load(root); e == nil {
		t.Fatal("blueprint escaped project")
	}
}
func TestServiceCyclesRejected(t *testing.T) {
	root := fixture(t)
	m, _, e := Load(root)
	if e != nil {
		t.Fatal(e)
	}
	m.Services = map[string]Service{"one": {Cwd: "code", Command: []string{"python", "entry.py"}, Probe: Probe{URL: "http://127.0.0.1:8000/"}, Requires: Requirements{Services: []string{"two"}}}, "two": {Cwd: "code", Command: []string{"python", "entry.py"}, Probe: Probe{URL: "http://127.0.0.1:8001/"}, Requires: Requirements{Services: []string{"one"}}}}
	if e = m.Validate(root); e == nil || !strings.Contains(e.Error(), "cycle") {
		t.Fatal("cycle was not rejected", e)
	}
}
