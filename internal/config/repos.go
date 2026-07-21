package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SetRepos rewrites only the `repos:` list in config.yaml, preserving every
// other key and all of the operator's comments. It round-trips through
// yaml.Node and replaces just the repos sequence node — a full Marshal of the
// Config struct would strip comments (C4: never destroy user content).
func SetRepos(base string, repos []string) error {
	path := filepath.Join(base, "config.yaml")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: reading config.yaml: %w", err)
	}

	var doc yaml.Node
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("config: parsing config.yaml: %w", err)
		}
	}
	if err := setMapKey(&doc, "repos", reposSeqNode(repos)); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2) // match culi's hand-written config style (default is 4)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("config: encoding config.yaml: %w", err)
	}
	_ = enc.Close()
	out := buf.Bytes()

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("config: writing config.yaml: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("config: replacing config.yaml: %w", err)
	}
	return nil
}

func reposSeqNode(repos []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, r := range repos {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: r})
	}
	return seq
}

// setMapKey sets key→value in the document's top-level mapping, creating the
// document, mapping, or key if absent. Existing sibling keys (and their
// comments) are left untouched.
func setMapKey(doc *yaml.Node, key string, value *yaml.Node) error {
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
	for i := 0; i+1 < len(mp.Content); i += 2 {
		if mp.Content[i].Value == key {
			mp.Content[i+1] = value
			return nil
		}
	}
	mp.Content = append(mp.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	return nil
}
