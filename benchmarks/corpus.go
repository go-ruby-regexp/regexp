// Package main (shared): loads the cross-engine benchmark corpus and builds the
// haystacks identically to the C and Ruby harnesses.
package main

import (
	"encoding/json"
	"os"
	"strings"
)

// Case is one (pattern, haystack) benchmark unit, shared verbatim by every
// engine harness via corpus.json.
type Case struct {
	Name    string `json:"name"`
	Desc    string `json:"desc"`
	Pattern string `json:"pattern"`
	Unit    string `json:"unit"`
	Repeat  int    `json:"repeat"`
	Suffix  string `json:"suffix"`
	RE2     bool   `json:"re2"`
}

// Haystack materialises the input the case is matched against.
func (c Case) Haystack() string {
	return strings.Repeat(c.Unit, c.Repeat) + c.Suffix
}

type corpus struct {
	Cases []Case `json:"cases"`
}

// loadCorpus reads corpus.json from the given path.
func loadCorpus(path string) ([]Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c corpus
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return c.Cases, nil
}
