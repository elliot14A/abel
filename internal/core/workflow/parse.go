package workflow

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/elliot14A/abel/internal/core/errs"
)

const opParse = "workflow.Parse"

// Parse decodes a workflow document. path is used only for diagnostics and to
// derive a name when the document has none; nothing is read from disk.
//
// Decode errors are returned as KindValidation and carry goccy's annotated
// source excerpt, so the message points at the offending line and column.
func Parse(path string, data []byte) (f File, err error) {
	// The decoder is third-party code fed untrusted input: abel parses whatever
	// workflow file a repository happens to contain. goccy v1.19.2 panics on
	// some malformed documents (a YAML tag where a sequence is expected, found
	// by FuzzParse and kept in testdata/fuzz). A panic escaping into `abel run`
	// would be a crash report instead of "line 3: invalid runs-on", so this one
	// boundary converts it into an ordinary validation error. This is the only
	// recover in the codebase; do not copy the pattern inward.
	defer func() {
		if r := recover(); r != nil {
			f = File{}
			err = errs.New(errs.KindValidation, opParse,
				"%s could not be decoded (the YAML parser rejected it at a low level): %v", path, r)
		}
	}()
	return parse(path, data)
}

func parse(path string, data []byte) (File, error) {
	astFile, err := parser.ParseBytes(data, parser.ParseComments)
	if err != nil {
		return File{}, errs.Wrap(err, errs.KindValidation, opParse,
			"%s is not valid YAML:\n%s", path, yaml.FormatError(err, false, true))
	}
	if len(astFile.Docs) == 0 || astFile.Docs[0].Body == nil {
		return File{}, errs.New(errs.KindValidation, opParse, "%s is empty", path)
	}

	var raw rawFile
	if err := yaml.NodeToValue(astFile.Docs[0].Body, &raw); err != nil {
		return File{}, errs.Wrap(err, errs.KindValidation, opParse,
			"%s is not a workflow file:\n%s", path, yaml.FormatError(err, false, true))
	}
	if len(raw.Jobs) == 0 {
		return File{}, errs.New(errs.KindValidation, opParse,
			"%s declares no jobs", path)
	}

	f := File{
		Path:     path,
		Name:     raw.Name,
		Env:      raw.Env,
		Defaults: raw.Defaults.run(),
		Jobs:     make(map[string]Job, len(raw.Jobs)),
		JobIDs:   jobIDsInOrder(astFile, raw.Jobs),
	}
	if f.Name == "" {
		f.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	for _, id := range f.JobIDs {
		f.Jobs[id] = raw.Jobs[id].toJob(id, astFile)
	}
	return f, nil
}

// jobIDsInOrder recovers declaration order from the AST, because decoding into
// a map loses it and every user-visible listing must be stable. If the AST walk
// finds nothing (an unusual document shape), it falls back to the map keys in
// sorted order rather than returning a non-deterministic list.
func jobIDsInOrder(f *ast.File, jobs map[string]rawJob) []string {
	ordered := make([]string, 0, len(jobs))
	seen := make(map[string]bool, len(jobs))

	if node := lookup(f, "$.jobs"); node != nil {
		if mapping, ok := node.(*ast.MappingNode); ok {
			for _, v := range mapping.Values {
				key := strings.Trim(v.Key.String(), `"'`)
				if _, known := jobs[key]; known && !seen[key] {
					ordered = append(ordered, key)
					seen[key] = true
				}
			}
		}
	}
	for id := range jobs {
		if !seen[id] {
			ordered = append(ordered, id)
		}
	}
	if len(ordered) != len(jobs) || len(seen) == 0 {
		// Fallback path: guarantee determinism even if the AST shape surprised us.
		sortStrings(ordered)
	}
	return ordered
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// lookup resolves a YAML path, returning nil when it does not match. Path
// syntax errors are treated as "not found": positions are a nicety, never a
// reason to fail a parse.
func lookup(f *ast.File, path string) ast.Node {
	p, err := yaml.PathString(path)
	if err != nil {
		return nil
	}
	node, err := p.FilterFile(f)
	if err != nil {
		return nil
	}
	return node
}

func lineOf(f *ast.File, path string) int {
	node := lookup(f, path)
	if node == nil || node.GetToken() == nil {
		return 0
	}
	return node.GetToken().Position.Line
}

// quotePathKey escapes a job ID for use in a YAML path expression. Job IDs are
// restricted by GitHub to [A-Za-z0-9_-], but a hand-written file can hold
// anything, and a bad path must degrade to "no line info", not to a panic.
func quotePathKey(key string) string {
	if strings.ContainsAny(key, `.[]'"$ `) {
		return "'" + strings.ReplaceAll(key, "'", "") + "'"
	}
	return key
}

// --- raw decoding types -----------------------------------------------------
//
// These mirror the YAML shapes exactly, including GitHub's polymorphic fields.
// They exist so the exported model above can stay clean and total.

type rawFile struct {
	Name     string            `yaml:"name"`
	Env      scalarMap         `yaml:"env"`
	Defaults rawDefaults       `yaml:"defaults"`
	Jobs     map[string]rawJob `yaml:"jobs"`
}

type rawDefaults struct {
	Run rawRunDefaults `yaml:"run"`
}

func (d rawDefaults) run() Defaults {
	return Defaults{Shell: d.Run.Shell, WorkingDirectory: d.Run.WorkingDirectory}
}

type rawRunDefaults struct {
	Shell            string `yaml:"shell"`
	WorkingDirectory string `yaml:"working-directory"`
}

type rawJob struct {
	Name      string       `yaml:"name"`
	RunsOn    stringList   `yaml:"runs-on"`
	Container rawContainer `yaml:"container"`
	Env       scalarMap    `yaml:"env"`
	Defaults  rawDefaults  `yaml:"defaults"`
	Steps     []rawStep    `yaml:"steps"`
	Needs     stringList   `yaml:"needs"`
	If        string       `yaml:"if"`
	Strategy  *struct {
		Matrix any `yaml:"matrix"`
	} `yaml:"strategy"`
}

func (r rawJob) toJob(id string, f *ast.File) Job {
	key := quotePathKey(id)
	job := Job{
		ID:          id,
		Name:        r.Name,
		RunsOn:      r.RunsOn,
		Container:   Container{Image: r.Container.Image, Env: r.Container.Env, Options: r.Container.Options},
		Env:         r.Env,
		Defaults:    r.Defaults.run(),
		Steps:       make([]Step, 0, len(r.Steps)),
		Needs:       r.Needs,
		If:          r.If,
		HasStrategy: r.Strategy != nil,
		Line:        lineOf(f, "$.jobs."+key),
	}
	if job.Name == "" {
		job.Name = id
	}
	for i, s := range r.Steps {
		job.Steps = append(job.Steps, Step{
			Name:             s.Name,
			Uses:             s.Uses,
			Run:              s.Run,
			Shell:            s.Shell,
			WorkingDirectory: s.WorkingDirectory,
			If:               s.If,
			Env:              s.Env,
			Line:             lineOf(f, fmt.Sprintf("$.jobs.%s.steps[%d]", key, i)),
		})
	}
	return job
}

type rawStep struct {
	Name             string    `yaml:"name"`
	Uses             string    `yaml:"uses"`
	Run              string    `yaml:"run"`
	Shell            string    `yaml:"shell"`
	WorkingDirectory string    `yaml:"working-directory"`
	If               string    `yaml:"if"`
	Env              scalarMap `yaml:"env"`
}

// rawContainer accepts both `container: image:tag` and the mapping form.
type rawContainer struct {
	Image   string
	Env     scalarMap
	Options string
}

func (c *rawContainer) UnmarshalYAML(data []byte) error {
	var image string
	if err := yaml.Unmarshal(data, &image); err == nil {
		c.Image = image
		return nil
	}
	var mapping struct {
		Image   string    `yaml:"image"`
		Env     scalarMap `yaml:"env"`
		Options string    `yaml:"options"`
	}
	if err := yaml.Unmarshal(data, &mapping); err != nil {
		return fmt.Errorf("container must be an image string or a mapping: %w", err)
	}
	c.Image, c.Env, c.Options = mapping.Image, mapping.Env, mapping.Options
	return nil
}

// stringList accepts both `runs-on: ubuntu-latest` and `runs-on: [self-hosted, linux]`.
type stringList []string

func (l *stringList) UnmarshalYAML(data []byte) error {
	var one string
	if err := yaml.Unmarshal(data, &one); err == nil {
		if one != "" {
			*l = stringList{one}
		}
		return nil
	}
	var many []string
	if err := yaml.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("expected a string or a list of strings: %w", err)
	}
	*l = many
	return nil
}

// scalarMap decodes an `env:` block whose values may be strings, numbers or
// booleans, normalising every value to the string an environment variable
// actually holds.
type scalarMap map[string]string

func (m *scalarMap) UnmarshalYAML(data []byte) error {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("expected a mapping of environment variables: %w", err)
	}
	out := make(scalarMap, len(raw))
	for k, v := range raw {
		s, err := scalarString(v)
		if err != nil {
			return fmt.Errorf("env %q: %w", k, err)
		}
		out[k] = s
	}
	*m = out
	return nil
}

func scalarString(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case uint64:
		return strconv.FormatUint(t, 10), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("value must be a string, number or boolean, got %T", v)
	}
}
