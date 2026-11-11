// Package manifest defines the versioned, deliberately small lab contract.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	SchemaVersion int                   `yaml:"schema_version" json:"schema_version"`
	ID            string                `yaml:"id" json:"id"`
	Name          string                `yaml:"name" json:"name"`
	Instructions  string                `yaml:"instructions" json:"instructions"`
	Runtime       Runtime               `yaml:"runtime" json:"runtime"`
	Config        map[string]Definition `yaml:"config" json:"config"`
	Platforms     map[string]Platform   `yaml:"platforms" json:"platforms"`
	Services      map[string]Service    `yaml:"services,omitempty" json:"services,omitempty"`
	Tasks         map[string]Task       `yaml:"tasks" json:"tasks"`
	Lifecycle     Lifecycle             `yaml:"lifecycle" json:"lifecycle"`
}
type Runtime struct {
	Kind         string     `yaml:"kind" json:"kind"`
	Python       string     `yaml:"python" json:"python"`
	Requirements string     `yaml:"requirements" json:"requirements"`
	Setup        [][]string `yaml:"setup,omitempty" json:"setup,omitempty"`
}
type Definition struct {
	Type        string   `yaml:"type,omitempty" json:"type,omitempty"`
	Default     string   `yaml:"default,omitempty" json:"default,omitempty"`
	Secret      bool     `yaml:"secret,omitempty" json:"secret,omitempty"`
	Enum        []string `yaml:"enum,omitempty" json:"enum,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
}
type Platform struct {
	Provider string `yaml:"provider" json:"provider"`
	Request  string `yaml:"request" json:"request"`
}
type Requirements struct {
	Platforms []string `yaml:"platforms,omitempty" json:"platforms,omitempty"`
	Services  []string `yaml:"services,omitempty" json:"services,omitempty"`
}
type Environment struct {
	Required    []string      `yaml:"required,omitempty" json:"required,omitempty"`
	Optional    []string      `yaml:"optional,omitempty" json:"optional,omitempty"`
	Conditional []Conditional `yaml:"conditional,omitempty" json:"conditional,omitempty"`
}
type Conditional struct {
	Operations []string          `yaml:"operations" json:"operations"`
	When       map[string]string `yaml:"when" json:"when"`
	Required   []string          `yaml:"required" json:"required"`
}
type Task struct {
	Blueprint   string               `yaml:"blueprint" json:"blueprint"`
	Source      string               `yaml:"source" json:"source"`
	Services    []string             `yaml:"services,omitempty" json:"services,omitempty"`
	Requires    Requirements         `yaml:"requires,omitempty" json:"requires,omitempty"`
	Environment Environment          `yaml:"environment,omitempty" json:"environment,omitempty"`
	Operations  map[string]Operation `yaml:"operations" json:"operations"`
}
type Operation struct {
	Mode         string       `yaml:"mode,omitempty" json:"mode,omitempty"`
	Cwd          string       `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	Command      []string     `yaml:"command" json:"command"`
	Effects      []string     `yaml:"effects,omitempty" json:"effects,omitempty"`
	Indexes      []Index      `yaml:"indexes,omitempty" json:"indexes,omitempty"`
	Requires     Requirements `yaml:"requires,omitempty" json:"requires,omitempty"`
	Required     []string     `yaml:"required,omitempty" json:"required,omitempty"`
	Verification string       `yaml:"verification,omitempty" json:"verification,omitempty"`
}
type Index struct {
	Config  string `yaml:"config" json:"config"`
	Default string `yaml:"default,omitempty" json:"default,omitempty"`
}
type Service struct {
	Cwd         string       `yaml:"cwd" json:"cwd"`
	Command     []string     `yaml:"command" json:"command"`
	Environment Environment  `yaml:"environment,omitempty" json:"environment,omitempty"`
	Requires    Requirements `yaml:"requires,omitempty" json:"requires,omitempty"`
	Probe       Probe        `yaml:"probe" json:"probe"`
}
type Probe struct {
	URL    string `yaml:"url" json:"url"`
	Method string `yaml:"method,omitempty" json:"method,omitempty"`
	Body   string `yaml:"body,omitempty" json:"body,omitempty"`
	Status int    `yaml:"status,omitempty" json:"status,omitempty"`
}
type Lifecycle struct {
	Up   []Step `yaml:"up" json:"up"`
	Down []Step `yaml:"down" json:"down"`
}
type Step struct {
	Platform string `yaml:"platform,omitempty" json:"platform,omitempty"`
	Service  string `yaml:"service,omitempty" json:"service,omitempty"`
	Action   string `yaml:"action" json:"action"`
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var keyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Load rejects unknown fields and multiple YAML documents before resolving paths.
func Load(root string) (*Manifest, string, error) {
	b, err := os.ReadFile(filepath.Join(root, "insta.yaml"))
	if err != nil {
		return nil, "", fmt.Errorf("read insta.yaml: %w", err)
	}
	if len(b) > 256*1024 {
		return nil, "", errors.New("insta.yaml exceeds 256 KiB")
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	var m Manifest
	if err = d.Decode(&m); err != nil {
		return nil, "", fmt.Errorf("invalid insta.yaml: %w", err)
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return nil, "", errors.New("insta.yaml must contain exactly one document")
	}
	if err = m.Validate(root); err != nil {
		return nil, "", err
	}
	h := sha256.New()
	h.Write(b)
	for _, name := range Keys(m.Platforms) {
		data, e := os.ReadFile(filepath.Join(root, m.Platforms[name].Request))
		if e != nil {
			return nil, "", e
		}
		h.Write(data)
	}
	return &m, hex.EncodeToString(h.Sum(nil)), nil
}

func (m *Manifest) Validate(root string) error {
	if m.SchemaVersion != 1 {
		return errors.New("unsupported schema_version: use 1")
	}
	if !nameRE.MatchString(m.ID) || m.Name == "" {
		return errors.New("manifest requires a valid id and name")
	}
	if m.Runtime.Kind != "python" || m.Runtime.Python != "3.12" {
		return errors.New("runtime must declare kind: python and python: '3.12'")
	}
	for _, p := range []string{m.Instructions, m.Runtime.Requirements} {
		if _, e := Path(root, p, false); e != nil {
			return e
		}
	}
	for _, cmd := range m.Runtime.Setup {
		if e := validateCommand(cmd); e != nil {
			return e
		}
	}
	for key, def := range m.Config {
		if !keyRE.MatchString(key) {
			return fmt.Errorf("invalid environment configuration key %q", key)
		}
		if def.Type != "" && def.Type != "string" && def.Type != "integer" && def.Type != "boolean" && def.Type != "number" {
			return fmt.Errorf("unsupported type for %s", key)
		}
		if def.Secret && def.Default != "" {
			return fmt.Errorf("secret %s cannot have a manifest default", key)
		}
		if strings.Contains(key, "PROVISION_") {
			return fmt.Errorf("%s belongs to CLI shell-only provider settings", key)
		}
	}
	for name, p := range m.Platforms {
		if !nameRE.MatchString(name) {
			return fmt.Errorf("invalid platform %q", name)
		}
		switch p.Provider {
		case "instaclustr-opensearch", "instaclustr-kafka", "instaclustr-cassandra", "instaclustr-clickhouse":
		default:
			return fmt.Errorf("unknown provider %q", p.Provider)
		}
		if _, e := Path(root, p.Request, false); e != nil {
			return e
		}
	}
	for name, s := range m.Services {
		if !nameRE.MatchString(name) {
			return fmt.Errorf("invalid service %q", name)
		}
		if _, e := Path(root, s.Cwd, true); e != nil {
			return e
		}
		if e := validateCommand(s.Command); e != nil {
			return e
		}
		if e := m.checkRequires(s.Requires); e != nil {
			return e
		}
		if e := m.checkEnv(s.Environment, nil); e != nil {
			return e
		}
		u, e := url.Parse(s.Probe.URL)
		if e != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" || u.Fragment != "" {
			return fmt.Errorf("service %s probe must use an explicit loopback HTTP address without user information", name)
		}
		port, e := strconv.Atoi(u.Port())
		if e != nil || port < 1 || port > 65535 {
			return fmt.Errorf("service %s probe must declare a valid port", name)
		}
		if s.Probe.Method != "" && s.Probe.Method != "GET" && s.Probe.Method != "POST" {
			return fmt.Errorf("invalid probe method for %s", name)
		}
		if s.Probe.Status != 0 && (s.Probe.Status < 100 || s.Probe.Status > 599) {
			return fmt.Errorf("invalid probe status for %s", name)
		}
	}
	if len(m.Tasks) == 0 {
		return errors.New("manifest has no tasks")
	}
	for name, t := range m.Tasks {
		if !nameRE.MatchString(name) {
			return fmt.Errorf("invalid task id %q", name)
		}
		if _, e := Path(root, t.Blueprint, false); e != nil {
			return e
		}
		if _, e := Path(root, t.Source, true); e != nil {
			return e
		}
		if e := m.checkRequires(t.Requires); e != nil {
			return e
		}
		if e := m.checkEnv(t.Environment, t.Operations); e != nil {
			return e
		}
		if e := m.checkRequires(Requirements{Services: t.Services}); e != nil {
			return e
		}
		for op, o := range t.Operations {
			if !nameRE.MatchString(op) {
				return fmt.Errorf("invalid operation %q", op)
			}
			if o.Mode != "" && o.Mode != "oneshot" && o.Mode != "interactive" {
				return fmt.Errorf("invalid mode for %s/%s", name, op)
			}
			if e := validateCommand(o.Command); e != nil {
				return e
			}
			cwd := o.Cwd
			if cwd == "" {
				cwd = t.Source
			}
			if _, e := Path(root, cwd, true); e != nil {
				return e
			}
			if e := m.checkRequires(o.Requires); e != nil {
				return e
			}
			keys := append([]string{}, o.Required...)
			for _, index := range o.Indexes {
				keys = append(keys, index.Config)
			}
			for _, key := range keys {
				if _, ok := m.Config[key]; !ok {
					return fmt.Errorf("%s/%s references unknown config %s", name, op, key)
				}
			}
		}
	}
	for _, steps := range [][]Step{m.Lifecycle.Up, m.Lifecycle.Down} {
		for _, s := range steps {
			if (s.Platform == "") == (s.Service == "") {
				return errors.New("lifecycle step must select one platform or service")
			}
			if s.Platform != "" {
				if _, ok := m.Platforms[s.Platform]; !ok {
					return fmt.Errorf("unknown lifecycle platform %s", s.Platform)
				}
				if s.Action != "ensure" && s.Action != "destroy_owned" {
					return fmt.Errorf("invalid platform action %s", s.Action)
				}
			}
			if s.Service != "" {
				if _, ok := m.Services[s.Service]; !ok {
					return fmt.Errorf("unknown lifecycle service %s", s.Service)
				}
				if s.Action != "start" && s.Action != "stop" {
					return fmt.Errorf("invalid service action %s", s.Action)
				}
			}
		}
	}
	// Dependency cycles are rejected before any service can be launched.
	seen := map[string]int{}
	var visit func(string) error
	visit = func(n string) error {
		if seen[n] == 1 {
			return fmt.Errorf("service dependency cycle at %s", n)
		}
		if seen[n] == 2 {
			return nil
		}
		seen[n] = 1
		for _, dep := range m.Services[n].Requires.Services {
			if e := visit(dep); e != nil {
				return e
			}
		}
		seen[n] = 2
		return nil
	}
	for n := range m.Services {
		if e := visit(n); e != nil {
			return e
		}
	}
	return nil
}

func validateCommand(args []string) error {
	if len(args) == 0 || args[0] == "" {
		return errors.New("command must be a nonempty argument array")
	}
	for _, a := range args {
		if strings.IndexByte(a, 0) >= 0 {
			return errors.New("command contains a NUL byte")
		}
	}
	return nil
}
func (m *Manifest) checkRequires(r Requirements) error {
	for _, n := range r.Platforms {
		if _, ok := m.Platforms[n]; !ok {
			return fmt.Errorf("unknown required platform %s", n)
		}
	}
	for _, n := range r.Services {
		if _, ok := m.Services[n]; !ok {
			return fmt.Errorf("unknown required service %s", n)
		}
	}
	return nil
}
func (m *Manifest) checkEnv(e Environment, ops map[string]Operation) error {
	keys := append(append([]string{}, e.Required...), e.Optional...)
	for _, c := range e.Conditional {
		if len(c.When) == 0 {
			return errors.New("environment condition needs when selectors")
		}
		for k := range c.When {
			keys = append(keys, k)
		}
		keys = append(keys, c.Required...)
		for _, op := range c.Operations {
			if ops == nil {
				if op != "start" {
					return fmt.Errorf("service condition references unknown operation %s", op)
				}
			} else if _, ok := ops[op]; !ok {
				return fmt.Errorf("condition references unknown operation %s", op)
			}
		}
	}
	for _, k := range keys {
		if _, ok := m.Config[k]; !ok {
			return fmt.Errorf("environment references unknown config %s", k)
		}
	}
	return nil
}

// Path confines reads/execution directories to the real project root, including links.
func Path(root, relative string, directory bool) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || strings.Contains(relative, "\\") {
		return "", fmt.Errorf("invalid project-relative path %q", relative)
	}
	p := filepath.Join(root, filepath.FromSlash(relative))
	realRoot, e := filepath.EvalSymlinks(root)
	if e != nil {
		return "", e
	}
	real, e := filepath.EvalSymlinks(p)
	if e != nil {
		return "", fmt.Errorf("missing manifest path %s: %w", relative, e)
	}
	rel, e := filepath.Rel(realRoot, real)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("manifest path escapes project: %s", relative)
	}
	info, e := os.Stat(real)
	if e != nil {
		return "", e
	}
	if info.IsDir() != directory {
		return "", fmt.Errorf("wrong file type for manifest path %s", relative)
	}
	return real, nil
}

func Keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func Required(e Environment, operation string, values map[string]string) []string {
	keys := append([]string{}, e.Required...)
	for _, c := range e.Conditional {
		match := false
		for _, op := range c.Operations {
			if op == operation {
				match = true
			}
		}
		for k, v := range c.When {
			if values[k] != v {
				match = false
			}
		}
		if match {
			keys = append(keys, c.Required...)
		}
	}
	return keys
}
