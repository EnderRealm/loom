package synthesis

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"
)

// tkTicket is one line of `tk query` JSONL, narrowed to what synthesis reads.
// The dates stay strings: tk omits updated and closed for a ticket written
// before it stored them, and a missing key must stay distinguishable from a
// time.
type tkTicket struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Type    string `json:"type"`
	Parent  string `json:"parent"`
	Title   string `json:"title"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	Closed  string `json:"closed"`
}

// runTK runs the tk CLI with args as argv — never through a shell — returning
// its stdout and forwarding its stderr to diag. A tk that is missing or exits
// non-zero is an error: an empty answer from a tk that did not run would read
// as a project that did nothing.
func runTK(diag io.Writer, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("tk", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stderr.Len() > 0 {
		// Labelled by the subcommand, past any leading global flag.
		label := ""
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				label = a
				break
			}
		}
		msg := stderr.String()
		if !strings.HasSuffix(msg, "\n") {
			msg += "\n"
		}
		fmt.Fprintf(diag, "tk %s: %s", label, msg)
	}
	if err != nil {
		return nil, fmt.Errorf("tk %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// queryProject reads every ticket in the central store through `tk query
// --all-projects` and keeps the project's namespace. The ids stay qualified,
// the form commit markers carry. A namespace holding no ticket at all is an
// error: it is far likelier a mistyped project than a real one, and an empty
// input would read as a project that did nothing.
func queryProject(diag io.Writer, project string) ([]tkTicket, error) {
	out, err := runTK(diag, "query", "--all-projects")
	if err != nil {
		return nil, err
	}
	var tickets []tkTicket
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var t tkTicket
		if err := dec.Decode(&t); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("tk query: decode: %w", err)
		}
		if ns, _ := ticket.ParseNamespacedID(t.ID); ns == project {
			tickets = append(tickets, t)
		}
	}
	if len(tickets) == 0 {
		return nil, fmt.Errorf("tk query: no ticket in namespace %q", project)
	}
	return tickets, nil
}

// tkTime parses a tk date. An absent key, tk's zero date and an unparseable
// value are all nil — never a zero time that a window test could admit. An
// unparseable one is reported, since it is tk output loom did not expect.
func tkTime(diag io.Writer, id, field, v string) *time.Time {
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		fmt.Fprintf(diag, "%s: unparseable %s %q — treated as absent\n", id, field, v)
		return nil
	}
	if t.IsZero() {
		return nil
	}
	return &t
}

// ticketBody is the part of a ticket's markdown body synthesis carries.
type ticketBody struct {
	Description        string
	Design             string
	AcceptanceCriteria string
}

// showBodies reads the bodies of ids, all in project, with one `tk show
// --metadata` per id. `tk query` does not carry the body sections its help
// lists and tk has no structured show, so the body comes from the rendered
// document instead: --metadata prints frontmatter, title and body without
// notes. One call per id because tk separates several documents only by their
// frontmatter, which a body can reproduce — a ticket could then supply the
// text of the ticket after it.
func showBodies(diag io.Writer, project string, ids []string) (map[string]ticketBody, error) {
	bodies := make(map[string]ticketBody, len(ids))
	for _, id := range ids {
		// --project= keeps a project name from ever being read as a flag.
		out, err := runTK(diag, "--project="+project, "show", "--metadata", id)
		if err != nil {
			return nil, err
		}
		body, err := parseBody(id, string(out))
		if err != nil {
			return nil, err
		}
		bodies[id] = body
	}
	return bodies, nil
}

// parseBody reads one rendered ticket: the frontmatter is skipped, the text
// between the `# title` line and the first `## ` heading is the description,
// and the Design and Acceptance Criteria sections run to the next `## `. A
// `## ` line inside a fenced code block is text, not a heading. The document
// has to open with id's frontmatter, so output for another ticket is refused
// rather than filed under this one.
func parseBody(id, doc string) (ticketBody, error) {
	_, bare := ticket.ParseNamespacedID(id)
	lines := strings.Split(doc, "\n")
	if len(lines) < 2 || lines[0] != "---" || lines[1] != "id: "+bare {
		return ticketBody{}, fmt.Errorf("tk show %s: output does not open with that ticket's frontmatter", id)
	}
	i := 2
	for i < len(lines) && lines[i] != "---" {
		i++
	}
	i++
	if i < len(lines) && strings.HasPrefix(lines[i], "# ") {
		i++
	}
	sections := map[string][]string{}
	current := ""
	fenced := false
	for ; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			fenced = !fenced
		}
		if h, ok := strings.CutPrefix(lines[i], "## "); ok && !fenced {
			current = strings.TrimSpace(h)
			continue
		}
		sections[current] = append(sections[current], lines[i])
	}
	text := func(name string) string {
		return strings.TrimSpace(strings.Join(sections[name], "\n"))
	}
	return ticketBody{
		Description:        text(""),
		Design:             text("Design"),
		AcceptanceCriteria: text("Acceptance Criteria"),
	}, nil
}
