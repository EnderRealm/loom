// Package escapes attributes done bug tickets to the commits that introduced
// them. The hand-labelled fixture in testdata/labels.jsonl is the ground truth
// that attribution is measured against.
package escapes

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// fixtureName is the fixture's path in the source tree, named in parse errors.
const fixtureName = "testdata/labels.jsonl"

// fixture is embedded so an installed binary, which has no source tree, can
// still score itself against the labels.
//
//go:embed testdata/labels.jsonl
var fixture []byte

// Label is one hand-labelled done bug: the commits that fixed it and either
// the commits that truly introduced it or the reason no commit did. Hashes
// are full and live in the registered repo of the ticket's project.
type Label struct {
	Ticket         string   `json:"ticket"`
	Fix            []string `json:"fix"`
	Introducing    []string `json:"introducing"`
	Unattributable string   `json:"unattributable"`
	Note           string   `json:"note"`
}

// Fixture parses the embedded label file, one object per line. Decoding is
// strict — an unknown field, a blank line or trailing data after the object
// is an error naming the line — so a misspelt key cannot silently read as an
// empty label.
func Fixture() ([]Label, error) {
	var labels []Label
	scanner := bufio.NewScanner(bytes.NewReader(fixture))
	for n := 1; scanner.Scan(); n++ {
		dec := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		dec.DisallowUnknownFields()
		var l Label
		if err := dec.Decode(&l); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", fixtureName, n, err)
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s:%d: trailing data after the label", fixtureName, n)
		}
		labels = append(labels, l)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", fixtureName, err)
	}
	return labels, nil
}
