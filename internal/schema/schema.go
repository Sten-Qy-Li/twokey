// Package schema defines the agents' JSON answers (SPEC section 6) and
// checks them. twokey validates every answer itself; it doesn't trust the
// CLIs' structured-output modes alone.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Sten-Qy-Li/twokey"
)

// The agent steps.
const (
	Audit       = "audit"
	Convergence = "convergence"
	Dispute     = "dispute"
	Fix         = "fix"
	Smoke       = "smoke" // twokey doctor --smoke
)

// Finding is one problem an audit found.
type Finding struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	Severity          string   `json:"severity"` // P1, P2, P3
	Kind              string   `json:"kind"`     // repo_fix, needs_decision, external_evidence
	Evidence          string   `json:"evidence"`
	FixHint           string   `json:"fix_hint"`
	Options           []string `json:"options"`
	RecommendedOption string   `json:"recommended_option"`
}

// Blocking reports whether the finding can stop the run: P1 or P2, and
// repo_fix or needs_decision (SPEC section 4).
func (f Finding) Blocking() bool {
	return (f.Severity == "P1" || f.Severity == "P2") && (f.Kind == "repo_fix" || f.Kind == "needs_decision")
}

// AuditResult is a blind audit's answer.
type AuditResult struct {
	AuditedCommit string    `json:"audited_commit"`
	Verdict       string    `json:"verdict"`
	Summary       string    `json:"summary"`
	Findings      []Finding `json:"findings"`
	LogEntry      string    `json:"log_entry_markdown"`
}

// MergedFinding is a finding in the convergence result.
type MergedFinding struct {
	Finding
	Sources       []string `json:"sources"`
	MergedFrom    []string `json:"merged_from"`
	Status        string   `json:"status"` // confirmed, disputed, noted
	DisputeReason string   `json:"dispute_reason"`
	RecursOf      string   `json:"recurs_of"`
}

// FromCodex reports whether Codex's blind audit raised the finding
// (DECISIONS.md D19).
func (m MergedFinding) FromCodex() bool {
	return slices.ContainsFunc(m.MergedFrom, func(id string) bool { return strings.Contains(id, "-CX-") })
}

// ConvergenceResult is the convergence step's answer.
type ConvergenceResult struct {
	AuditedCommit string          `json:"audited_commit"`
	Verdict       string          `json:"verdict"`
	Summary       string          `json:"summary"`
	Findings      []MergedFinding `json:"findings"`
	LogEntry      string          `json:"log_entry_markdown"`
}

// Recheck is Codex's answer about one disputed finding.
type Recheck struct {
	ID       string `json:"id"`
	Decision string `json:"decision"` // withdraw, maintain
	Evidence string `json:"evidence"`
}

// DisputeResult is the dispute step's answer.
type DisputeResult struct {
	Rechecks []Recheck `json:"rechecks"`
	LogEntry string    `json:"log_entry_markdown"`
}

// Done is a finding the fix step fixed.
type Done struct {
	ID          string `json:"id"`
	Change      string `json:"change"`
	VerifiedHow string `json:"verified_how"`
}

// NotDone is a finding the fix step left alone.
type NotDone struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// SmokeResult is the answer to twokey doctor --smoke.
type SmokeResult struct {
	Wrote      bool        `json:"wrote"`
	HTTPStatus int         `json:"http_status"`
	Reads      []SmokeRead `json:"reads"`
	Note       string      `json:"note"`
}

// SmokeRead is one file the smoke test asked an agent to read.
type SmokeRead struct {
	Path          string `json:"path"`
	FirstBytesHex string `json:"first_bytes_hex"`
}

// FixResult is the fix step's answer.
type FixResult struct {
	BaseCommit string    `json:"base_commit"`
	Commits    []string  `json:"commits"`
	Done       []Done    `json:"done"`
	NotDone    []NotDone `json:"not_done"`
	LogEntry   string    `json:"log_entry_markdown"`
}

// Raw returns a step's schema file as shipped.
func Raw(step string) ([]byte, error) {
	return fs.ReadFile(twokey.Schemas, step+".schema.json")
}

// Minified returns a step's schema without spaces, for claude --json-schema.
func Minified(step string) (string, error) {
	raw, err := Raw(step)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Decode checks an answer against its step's schema and decodes it into
// out. It returns every problem found, each with its path.
func Decode(step string, data []byte, out any) []string {
	raw, err := Raw(step)
	if err != nil {
		return []string{err.Error()}
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		return []string{"the shipped schema is broken: " + err.Error()}
	}
	if probs := Validate(s, data); len(probs) > 0 {
		return probs
	}
	if err := json.Unmarshal(data, out); err != nil {
		return []string{"the answer doesn't decode: " + err.Error()}
	}
	return nil
}

// Validate checks JSON data against a schema that uses only type,
// properties, required, additionalProperties, enum, items, pattern,
// minItems and maxItems (DECISIONS.md D18).
func Validate(s map[string]any, data []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return []string{"the answer is not valid JSON: " + err.Error()}
	}
	if dec.More() {
		return []string{"the answer has more text after the JSON object"}
	}
	var probs []string
	validate(s, v, "answer", &probs)
	return probs
}

func validate(s map[string]any, v any, path string, probs *[]string) {
	add := func(format string, args ...any) { *probs = append(*probs, path+": "+fmt.Sprintf(format, args...)) }

	if t, ok := s["type"].(string); ok && !hasType(v, t) {
		add("must be %s, not %s", typeWord(t), describe(v))
		return
	}
	if enum, ok := s["enum"].([]any); ok && !slices.Contains(enum, v) {
		var opts []string
		for _, e := range enum {
			opts = append(opts, fmt.Sprint(e))
		}
		add("must be one of %s (got %s)", strings.Join(opts, ", "), describe(v))
	}
	if p, ok := s["pattern"].(string); ok {
		if str, isStr := v.(string); isStr {
			if re, err := regexp.Compile(p); err == nil && !re.MatchString(str) {
				add("%q doesn't match the pattern %s", str, p)
			}
		}
	}

	switch val := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		req, _ := s["required"].([]any)
		for _, r := range req {
			if name, ok := r.(string); ok {
				if _, present := val[name]; !present {
					add("%s is missing", name)
				}
			}
		}
		if ap, ok := s["additionalProperties"].(bool); ok && !ap {
			for _, k := range sortedKeys(val) {
				if _, known := props[k]; !known {
					add("%s is not allowed", k)
				}
			}
		}
		for _, k := range sortedKeys(val) {
			if sub, ok := props[k].(map[string]any); ok {
				validate(sub, val[k], path+"."+k, probs)
			}
		}
	case []any:
		if n, ok := number(s["minItems"]); ok && len(val) < n {
			add("must have at least %d items (has %d)", n, len(val))
		}
		if n, ok := number(s["maxItems"]); ok && len(val) > n {
			add("must have at most %d items (has %d)", n, len(val))
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, item := range val {
				validate(items, item, fmt.Sprintf("%s[%d]", path, i), probs)
			}
		}
	}
}

func hasType(v any, t string) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, err := n.Int64()
		return err == nil
	}
	return true
}

func typeWord(t string) string {
	switch t {
	case "object":
		return "an object"
	case "array":
		return "a list"
	case "string":
		return "text"
	case "integer":
		return "a whole number"
	default:
		return "a " + t
	}
}

func describe(v any) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case string:
		return fmt.Sprintf("%q", val)
	case map[string]any:
		return "an object"
	case []any:
		return "a list"
	default:
		return fmt.Sprint(val)
	}
}

func number(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
