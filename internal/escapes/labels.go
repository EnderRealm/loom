// Package escapes attributes done bug tickets to the commits that introduced
// them. The hand-labelled fixture in testdata/labels.jsonl is the ground truth
// that attribution is measured against.
package escapes

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

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

// LoadLabels reads a JSONL label file, one object per line. Decoding is
// strict — an unknown field, a blank line or trailing data after the object
// is an error naming the line — so a misspelt key cannot silently read as an
// empty label.
func LoadLabels(path string) ([]Label, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var labels []Label
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		dec := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		dec.DisallowUnknownFields()
		var l Label
		if err := dec.Decode(&l); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s:%d: trailing data after the label", path, n)
		}
		labels = append(labels, l)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return labels, nil
}
