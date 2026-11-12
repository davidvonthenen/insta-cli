package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/instaclustr/insta-cli/internal/bootstrap"
	"github.com/instaclustr/insta-cli/internal/manifest"
	"github.com/instaclustr/insta-cli/internal/state"
)

// These small synthetic sources exercise ownership of the lab contract. They
// deliberately differ from the catalog's teaching labs and are never embedded
// in the CLI executable or used to fill gaps in a downloaded source archive.
const upstreamFixtureManifest = `schema_version: 1
id: rag-retrieval
name: Repository-owned test lab
instructions: AGENTS.md
runtime:
  kind: python
  python: "3.12"
  requirements: support/requirements.txt
config:
  OPENSEARCH_TEST_INDEX:
    default: upstream-index
platforms: {}
tasks:
  repository-check:
    name: Repository-owned check
    description: Verify the operation supplied by the source repository.
    blueprint: tasks/check.md
    source: scripts/check
    operations:
      validate:
        command: [python, -m, src.validate]
        effects: [local-read]
lifecycle:
  up: []
  down: []
`

func upstreamFixtureFiles() map[string]string {
	return map[string]string{
		"insta.yaml":                      upstreamFixtureManifest,
		"insta.schema.json":               `{"description":"Repository-owned schema fixture"}` + "\n",
		"AGENTS.md":                       "# Repository-owned agent instructions\n",
		"INSTA.md":                        "# Repository-owned workflow\n",
		"README.md":                       "# Repository-owned lab documentation\n",
		".gitignore":                      "# Repository-owned ignore rules\nbuild/\n",
		".env.example":                    "OPENAI_API_KEY=\n",
		".python-version":                 "3.12\n",
		"tasks/check.md":                  "# Repository-owned task blueprint\n",
		"scripts/check/src/validate.py":   "print('repository check')\n",
		"support/requirements.txt":        "# Repository-owned dependencies\n",
		"support/validate_setup.py":       "print('repository setup check')\n",
		"support/common/config.py":        "source = 'repository'\n",
		".agents/skills/example/SKILL.md": "# Repository-owned agent guidance\n",
		"scripts/check/src/common":        "../../../support/common",
	}
}

func upstreamFixtureArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := &zip.FileHeader{Name: filepath.Base(bootstrap.DefaultRepository.Origin) + "-" + bootstrapFixtureCommit + "/rag-retrieval/" + name, Method: zip.Deflate}
		if name == "scripts/check/src/common" {
			h.SetMode(os.ModeSymlink | 0777)
		} else {
			h.SetMode(0644)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestNewPreservesRepositoryOwnedLabFiles(t *testing.T) {
	files := upstreamFixtureFiles()
	archive := upstreamFixtureArchive(t, files)
	a, output, diagnostic, trace, requests := bootstrapHarnessWithArchive(t, archive)
	if code := a.Execute(context.Background(), []string{"new", "from-repository", "--lab", "rag-retrieval", "--json"}); code != 0 {
		t.Fatalf("new exit=%d\n%s\n%s", code, output, diagnostic)
	}
	root, err := filepath.Abs("from-repository")
	if err != nil {
		t.Fatal(err)
	}
	for name, expected := range files {
		if name == "scripts/check/src/common" {
			actual, err := os.Readlink(filepath.Join(root, name))
			if err != nil || actual != expected {
				t.Errorf("source symlink changed: %q, %v", actual, err)
			}
			continue
		}
		actual, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Error(err)
			continue
		}
		if name == ".gitignore" {
			if !strings.HasPrefix(string(actual), expected) || !strings.Contains(string(actual), ".insta/\n") {
				t.Error("managed ignore rules did not preserve repository rules")
			}
		} else if string(actual) != expected {
			t.Errorf("repository file %s was rewritten", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "deploy-opensearch.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("CLI supplied a platform request absent from repository source", err)
	}
	m, approved, err := manifest.Load(root)
	if err != nil || m.Name != "Repository-owned test lab" || len(m.Tasks) != 1 || m.Tasks["repository-check"].Name != "Repository-owned check" {
		t.Fatal("repository manifest was not authoritative", err)
	}
	p, err := state.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	expectedSource := map[string]string{
		"origin": bootstrap.DefaultRepository.Origin, "ref_type": "heads", "ref": "main",
		"commit": bootstrapFixtureCommit, "source_path": "rag-retrieval",
		"sha256": hex.EncodeToString(digest[:]), "cli_version": Version,
	}
	if len(p.Source) != len(expectedSource) {
		t.Fatalf("unexpected source provenance: %#v", p.Source)
	}
	for key, value := range expectedSource {
		if p.Source[key] != value {
			t.Errorf("source %s=%q; want %q", key, p.Source[key], value)
		}
	}
	if p.Stack().ApprovedManifest != approved || p.Stack().RuntimeStatus != "ready" || p.Stack().Config["OPENSEARCH_TEST_INDEX"] != "upstream-index-"+p.ID[:8]+"-dev" {
		t.Fatal("managed state was not initialized from repository manifest")
	}
	if *requests != 1 {
		t.Fatalf("archive requests=%d; want 1", *requests)
	}
	invocations, err := os.ReadFile(trace)
	if err != nil || !bytes.Contains(invocations, []byte("arg=pip")) || bytes.Contains(invocations, []byte("arg=src.validate")) {
		t.Fatal("new did not restrict execution to runtime preparation", err)
	}
}

func TestNewRejectsIncompleteRepositoryLabBeforePublishing(t *testing.T) {
	cases := map[string]func(map[string]string){
		"missing manifest":   func(files map[string]string) { delete(files, "insta.yaml") },
		"malformed manifest": func(files map[string]string) { files["insta.yaml"] = "schema_version: [" },
		"unsupported schema": func(files map[string]string) {
			files["insta.yaml"] = strings.Replace(files["insta.yaml"], "schema_version: 1", "schema_version: 2", 1)
		},
		"different lab id": func(files map[string]string) {
			files["insta.yaml"] = strings.Replace(files["insta.yaml"], "id: rag-retrieval", "id: agentic-rag", 1)
		},
		"missing requirements":   func(files map[string]string) { delete(files, "support/requirements.txt") },
		"missing task blueprint": func(files map[string]string) { delete(files, "tasks/check.md") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			files := upstreamFixtureFiles()
			mutate(files)
			a, output, diagnostic, trace, requests := bootstrapHarnessWithArchive(t, upstreamFixtureArchive(t, files))
			if code := a.Execute(context.Background(), []string{"new", "rejected", "--lab", "rag-retrieval", "--json"}); code != 3 {
				t.Fatalf("new exit=%d\n%s\n%s", code, output, diagnostic)
			}
			var result struct {
				OK    bool `json:"ok"`
				Error struct {
					Category string `json:"category"`
					Message  string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.OK || result.Error.Category != "bootstrap" || !strings.Contains(result.Error.Message, "labs-mirror") || !strings.Contains(result.Error.Message, "insta.yaml") {
				t.Fatalf("missing actionable source validation error: %s (%v)", output, err)
			}
			if _, err := os.Lstat("rejected"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid source left a published project", err)
			}
			if stages, err := filepath.Glob(".insta-new-*"); err != nil || len(stages) != 0 {
				t.Fatal("invalid source left a staging directory", err)
			}
			if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) || *requests != 1 {
				t.Fatal("invalid source reached runtime setup or repeated download", err)
			}
		})
	}
}

func TestNewFromVerifiedOfflineCachePreservesSourceProvenance(t *testing.T) {
	a, output, diagnostic, _, requests := bootstrapHarnessWithArchive(t, upstreamFixtureArchive(t, upstreamFixtureFiles()))
	if code := a.Execute(context.Background(), []string{"new", "online", "--lab", "rag-retrieval", "--json"}); code != 0 {
		t.Fatalf("cache setup exit=%d\n%s\n%s", code, output, diagnostic)
	}
	output.Reset()
	diagnostic.Reset()
	a.Source.HTTP = &http.Client{Transport: lifecycleTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("fixture source lookup unavailable")
	})}
	if code := a.Execute(context.Background(), []string{"new", "offline", "--lab", "rag-retrieval", "--json"}); code != 0 {
		t.Fatalf("offline new exit=%d\n%s\n%s", code, output, diagnostic)
	}
	p, err := state.Load("offline")
	if err != nil || p.Source["offline_cache"] != "true" || p.Source["commit"] != bootstrapFixtureCommit || p.Source["origin"] != bootstrap.DefaultRepository.Origin || len(p.Source["sha256"]) != 64 {
		t.Fatal("verified cache source provenance was lost", err)
	}
	if _, exists := p.Source["overlay_sha256"]; exists {
		t.Fatal("offline project recorded removed overlay provenance")
	}
	if *requests != 1 || !strings.Contains(diagnostic.String(), "using verified cached") {
		t.Fatal("offline bootstrap did not report and reuse the verified source")
	}
}
