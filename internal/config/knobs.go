package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// knobKind says how a console-supplied string value is parsed into a YAML node.
type knobKind int

const (
	kindString knobKind = iota
	kindInt
	kindFloat
	kindList // comma-separated → sequence
)

type knobSpec struct {
	path []string // YAML key path, e.g. {"learn", "oauth_token_file"}
	kind knobKind
}

// knobs is the whitelist of config.yaml keys the review console may write.
// Anything not listed here is silently ignored — the console can never write an
// arbitrary key, and secrets (which live in files, not config.yaml) are never
// among them.
var knobs = map[string]knobSpec{
	"push_budget":            {[]string{"push_budget"}, kindInt},
	"baseline_budget":        {[]string{"baseline_budget"}, kindInt},
	"candidate_ttl_days":     {[]string{"learn", "candidate_ttl_days"}, kindInt},
	"daily_usd_cap":          {[]string{"learn", "daily_usd_cap"}, kindFloat},
	"daily_call_cap":         {[]string{"learn", "daily_call_cap"}, kindInt},
	"provider":               {[]string{"learn", "provider"}, kindString},
	"oauth_token_file":       {[]string{"learn", "oauth_token_file"}, kindString},
	"anthropic_api_key_file": {[]string{"learn", "anthropic_api_key_file"}, kindString},
	"extra_acks":             {[]string{"extra_acks"}, kindList},
	"extra_stopwords":        {[]string{"extra_stopwords"}, kindList},
}

// SetKnobs writes the given safe knobs into config.yaml in place, preserving
// comments and every unlisted key (C4: never destroy user content). Values
// arrive as strings (the console's form fields); non-whitelisted keys are
// ignored. Returns the number of keys applied. A parse error on any value
// aborts the whole write — config.yaml is left untouched.
func SetKnobs(base string, values map[string]string) (int, error) {
	doc, path, err := loadConfigDoc(base)
	if err != nil {
		return 0, err
	}
	applied := 0
	for k, v := range values {
		spec, ok := knobs[k]
		if !ok {
			continue
		}
		node, err := scalarNode(spec.kind, v)
		if err != nil {
			return 0, fmt.Errorf("config: knob %s: %w", k, err)
		}
		if err := setNestedKey(doc, spec.path, node); err != nil {
			return 0, err
		}
		applied++
	}
	if applied == 0 {
		return 0, nil
	}
	if err := writeConfigDoc(path, doc); err != nil {
		return 0, err
	}
	return applied, nil
}

func scalarNode(kind knobKind, v string) (*yaml.Node, error) {
	v = strings.TrimSpace(v)
	switch kind {
	case kindInt:
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("want an integer, got %q", v)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(n)}, nil
	case kindFloat:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("want a number, got %q", v)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(f, 'f', -1, 64)}, nil
	case kindList:
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for part := range strings.SplitSeq(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p})
			}
		}
		return seq, nil
	default: // kindString
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	}
}

// setNestedKey sets value at a nested key path in the document's mapping,
// creating intermediate mappings and keys as needed. Sibling keys — and their
// comments — are left untouched.
func setNestedKey(doc *yaml.Node, path []string, value *yaml.Node) error {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	mp := doc.Content[0]
	if mp.Kind != yaml.MappingNode {
		return fmt.Errorf("config: config.yaml root is not a mapping")
	}
	for i, key := range path {
		last := i == len(path)-1
		valIdx := -1
		for j := 0; j+1 < len(mp.Content); j += 2 {
			if mp.Content[j].Value == key {
				valIdx = j + 1
				break
			}
		}
		if last {
			if valIdx >= 0 {
				// Carry the old value node's comments onto the replacement so an
				// inline "# note" or leading comment on this key survives the write.
				old := mp.Content[valIdx]
				value.HeadComment, value.LineComment, value.FootComment = old.HeadComment, old.LineComment, old.FootComment
				mp.Content[valIdx] = value
			} else {
				mp.Content = append(mp.Content, keyNode(key), value)
			}
			return nil
		}
		if valIdx >= 0 {
			child := mp.Content[valIdx]
			if child.Kind != yaml.MappingNode {
				return fmt.Errorf("config: %q is not a mapping in config.yaml", key)
			}
			mp = child
			continue
		}
		child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mp.Content = append(mp.Content, keyNode(key), child)
		mp = child
	}
	return nil
}

func keyNode(k string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}
}

// loadConfigDoc reads config.yaml as a comment-preserving node tree (empty doc
// when the file is absent). writeConfigDoc round-trips it back atomically.
func loadConfigDoc(base string) (*yaml.Node, string, error) {
	path := filepath.Join(base, "config.yaml")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("config: reading config.yaml: %w", err)
	}
	doc := &yaml.Node{}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, doc); err != nil {
			return nil, "", fmt.Errorf("config: parsing config.yaml: %w", err)
		}
	}
	return doc, path, nil
}

func writeConfigDoc(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2) // match culi's hand-written config style (default is 4)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("config: encoding config.yaml: %w", err)
	}
	_ = enc.Close()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("config: writing config.yaml: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("config: replacing config.yaml: %w", err)
	}
	return nil
}
