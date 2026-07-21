package config

import "gopkg.in/yaml.v3"

// SetRepos rewrites only the `repos:` list in config.yaml, preserving every
// other key and all of the operator's comments. It round-trips through
// yaml.Node and replaces just the repos sequence node — a full Marshal of the
// Config struct would strip comments (C4: never destroy user content).
func SetRepos(base string, repos []string) error {
	doc, path, err := loadConfigDoc(base)
	if err != nil {
		return err
	}
	if err := setNestedKey(doc, []string{"repos"}, reposSeqNode(repos)); err != nil {
		return err
	}
	return writeConfigDoc(path, doc)
}

func reposSeqNode(repos []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, r := range repos {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: r})
	}
	return seq
}
