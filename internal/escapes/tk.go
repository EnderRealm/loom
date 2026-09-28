package escapes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"
)

// Store is what attribution reads from the tk central store: the qualified
// ids of every done bug, and every project namespace holding any ticket.
// Both are sorted.
type Store struct {
	DoneBugs []string
	Projects []string
}

// LoadStore reads every ticket in the central store through `tk query
// --all-projects`. A tk that is missing or exits non-zero is an error: an
// empty store would read as a project history with no bugs.
func LoadStore() (Store, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("tk", "query", "--all-projects")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Store{}, fmt.Errorf("tk query --all-projects: %v: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	var st Store
	projects := map[string]bool{}
	dec := json.NewDecoder(&stdout)
	for {
		var tk struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Type   string `json:"type"`
		}
		if err := dec.Decode(&tk); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return Store{}, fmt.Errorf("tk query: decode: %w", err)
		}
		if project, _ := ticket.ParseNamespacedID(tk.ID); project != "" {
			projects[project] = true
		}
		if tk.Status == string(ticket.StatusDone) && tk.Type == string(ticket.TypeBug) {
			st.DoneBugs = append(st.DoneBugs, tk.ID)
		}
	}
	for p := range projects {
		st.Projects = append(st.Projects, p)
	}
	sort.Strings(st.DoneBugs)
	sort.Strings(st.Projects)
	return st, nil
}
