package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/instaclustr/insta-cli/internal/bootstrap"
	"github.com/instaclustr/insta-cli/internal/manifest"
	"github.com/instaclustr/insta-cli/internal/state"
)

// A clean, extracted upstream lab is an external fixture, never embedded in the
// CLI. Wrap it in the repository archive shape to exercise the real download,
// extraction, validation, project initialization, and managed-runtime paths.
// Only the HTTP transport and Python/Codex executables are test doubles.
func TestNewFromSuppliedLabPreservesSourceOptional(t *testing.T) {
	sourceRoot := os.Getenv("INSTA_TEST_LAB_ROOT")
	if sourceRoot == "" {
		t.Skip("set INSTA_TEST_LAB_ROOT to a clean extracted lab containing insta.yaml")
	}
	sourceRoot, err := filepath.Abs(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	m, approved, err := manifest.Load(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	lab, err := bootstrap.Lookup(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	repositoryRoot := path.Base(bootstrap.DefaultRepository.Origin) + "-" + bootstrapFixtureCommit
	type original struct {
		mode fs.FileMode
		data []byte
	}
	files := map[string]original{}
	err = filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = repositoryRoot + "/" + lab.SourcePath + "/" + filepath.ToSlash(relative)
		header.Method = zip.Deflate
		if info.IsDir() {
			header.Name += "/"
		}
		var data []byte
		switch {
		case info.IsDir():
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			data = []byte(target)
		case info.Mode().IsRegular():
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported fixture file %s", relative)
		}
		writer, err := z.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err = writer.Write(data); err != nil {
			return err
		}
		files[relative] = original{mode: info.Mode(), data: data}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	a, output, diagnostic, trace, requests := bootstrapHarnessWithArchive(t, archive.Bytes())
	if code := a.Execute(context.Background(), []string{"new", "upstream-project", "--lab", lab.ID, "--json"}); code != 0 {
		t.Fatalf("new exit=%d\n%s\n%s", code, output, diagnostic)
	}
	destination, err := filepath.Abs("upstream-project")
	if err != nil {
		t.Fatal(err)
	}
	p, err := state.Load(destination)
	if err != nil {
		t.Fatal(err)
	}
	if p.Lab != m.ID || p.Stack().ApprovedManifest != approved || p.Stack().RuntimeStatus != "ready" {
		t.Fatal("upstream manifest or managed-runtime initialization changed")
	}
	if _, exists := p.Source["overlay_sha256"]; exists {
		t.Fatal("new project contains retired overlay provenance")
	}
	if p.Source["origin"] != bootstrap.DefaultRepository.Origin || p.Source["cli_version"] != Version || len(p.Source["sha256"]) != 64 || *requests != 1 {
		t.Fatal("source provenance or download count is incorrect")
	}
	if len(p.Stack().Platforms) != 0 || len(p.Stack().Services) != 0 || len(p.Stack().Operations) != 0 {
		t.Fatal("project creation ran lab operations or provisioned services")
	}
	for relative, before := range files {
		target := filepath.Join(destination, relative)
		info, err := os.Lstat(target)
		if err != nil {
			t.Fatalf("upstream path missing: %s: %v", relative, err)
		}
		if before.mode.Type() != info.Mode().Type() {
			t.Fatalf("upstream path type changed: %s", relative)
		}
		if info.IsDir() {
			continue
		}
		if before.mode&os.ModeSymlink != 0 {
			target, err := os.Readlink(target)
			if err != nil || target != string(before.data) {
				t.Fatalf("upstream link changed: %s: %v", relative, err)
			}
			continue
		}
		after, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if relative == ".gitignore" {
			if !bytes.HasPrefix(after, before.data) || !bytes.Contains(after, []byte("# insta managed state and local credentials")) {
				t.Fatal("project ignore rules replaced upstream content")
			}
		} else if !bytes.Equal(after, before.data) {
			t.Fatalf("upstream file was rewritten: %s", relative)
		}
		if (before.mode.Perm()&0111 != 0) != (info.Mode().Perm()&0111 != 0) {
			t.Fatalf("upstream executable flag changed: %s", relative)
		}
	}
	// Creation may add local bookkeeping, but never substitute a lab integration
	// file (manifest, schema, instructions, deployment request, or helper script).
	err = filepath.WalkDir(destination, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(destination, path)
		if err != nil {
			return err
		}
		if relative == ".insta" {
			return filepath.SkipDir
		}
		if relative == "." || relative == ".env" || relative == ".gitignore" {
			return nil
		}
		if _, exists := files[relative]; !exists {
			return fmt.Errorf("CLI injected an upstream-owned file: %s", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	invocations, err := os.ReadFile(trace)
	if err != nil || !bytes.Contains(invocations, []byte("arg=venv")) || !bytes.Contains(invocations, []byte("arg=pip")) {
		t.Fatal("managed runtime was not prepared", err)
	}
	t.Chdir(destination)
	output.Reset()
	diagnostic.Reset()
	if code := a.Execute(context.Background(), []string{"task", "list", "--json"}); code != 0 {
		t.Fatalf("upstream task list exit=%d: %s\n%s", code, output, diagnostic)
	}
	// Historical source metadata remains readable as an opaque record; no
	// bundled-template lookup or hash migration is needed for existing projects.
	p.Source["overlay_sha256"] = strings.Repeat("a", 64)
	if err = state.Save(destination, p); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	diagnostic.Reset()
	if code := a.Execute(context.Background(), []string{"task", "list", "--json"}); code != 0 {
		t.Fatalf("legacy source metadata exit=%d: %s\n%s", code, output, diagnostic)
	}
	t.Logf("Validated %s: %d upstream paths retained, %d tasks, manifest digest %s", lab.ID, len(files), len(m.Tasks), approved)
}
