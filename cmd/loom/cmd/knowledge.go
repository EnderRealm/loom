package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"loom/internal/extract"
	"loom/internal/knowledge/store"
)

// The plan's op vocabulary. Declared rather than spelled at each comparison:
// the Python client under extractors/ writes these strings, so they are a
// contract between the two languages (docs/knowledge-store-writes.md).
const (
	opWrite  = "write"
	opAppend = "append"
	opRemove = "remove"
	opRename = "rename"
	opTouch  = "touch"
)

// writePlan is one unit of work against the knowledge store, as a writer that is
// not Go states it: a commit subject and the changes to apply under it. The
// subcommand exists so that writer needs no git of its own — the store's rules
// live in internal/knowledge/store and are applied here on its behalf.
type writePlan struct {
	Message string   `json:"message"`
	Changes []change `json:"changes"`
}

// change is one op. The fields are a union over the vocabulary rather than five
// shapes: a plan arrives as JSON from a language with no sum type, and rejecting
// an op whose required field is missing is validation's job, not the decoder's.
type change struct {
	Op        string `json:"op"`
	Path      string `json:"path"`
	Body      string `json:"body"`
	Text      string `json:"text"`
	From      string `json:"from"`
	To        string `json:"to"`
	Droppable bool   `json:"droppable"`
}

// knowledgeCmd is built by a constructor so a test can drive the plan through a
// command of its own, rather than the registered one's stdin.
var knowledgeCmd = newKnowledgeCmd()

func newKnowledgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "knowledge",
		Short: "Operate on the durable knowledge store",
	}
	cmd.AddCommand(newKnowledgeWriteCmd())
	cmd.AddCommand(newKnowledgeScopeCmd())
	return cmd
}

func newKnowledgeWriteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "write",
		Short: "Apply one JSON write plan, read from stdin, to the knowledge store",
		Long: "Reads one JSON plan on stdin and applies it to ~/.loom/knowledge, committing " +
			"every path it touched as one record and pushing that commit:\n\n" +
			`  {"message": "extract abc | loom | 2 truth candidate(s)",` + "\n" +
			`   "changes": [{"op": "write", "path": "...", "body": "..."}]}` + "\n\n" +
			"Ops: write (path, body), append (path, text), remove (path), rename (from, to, " +
			"droppable), touch (path). Prints {\"warn\": \"<reason or empty>\", \"push_warn\": " +
			"\"<reason or empty>\"} on stdout: a commit that did not land is a warning, since " +
			"the writes did, and so is a commit that landed but was not pushed.",
		Args: cobra.NoArgs,
		// The caller is a program, not a person: a refused plan is one line on
		// stderr — printed once, by Execute — rather than cobra's error plus a
		// usage dump the writer has no use for.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read plan: %w", err)
			}
			var plan writePlan
			if err := json.Unmarshal(raw, &plan); err != nil {
				return fmt.Errorf("parse plan: %w", err)
			}
			// Validated whole before anything is applied: a plan that names an
			// op we would refuse half way through would leave the store holding
			// the changes before it and no record of what was meant to follow.
			if err := validatePlan(plan); err != nil {
				return err
			}
			warn, applyErr := store.Apply(plan.Message, func(tx *store.Tx) error {
				return applyChanges(tx, plan.Changes)
			})
			// Printed whatever happened, so a caller that also has to report a
			// failed write learns from one place whether the record landed and
			// whether it was published.
			out, err := json.Marshal(map[string]string{
				"warn":      warn.NotCommitted,
				"push_warn": warn.NotPushed,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return applyErr
		},
	}
}

// gitkeepName is the placeholder that makes a new scope survive a clone: git
// tracks files and not directories, so an empty truths/<name>/ would exist only
// on the machine that ran this. Deliberately not a *.md file — internal/knowledge
// walks the store for markdown, and a placeholder must not read back as a truth.
const gitkeepName = ".gitkeep"

func newKnowledgeScopeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scope",
		Short: "Manage the knowledge store's per-project scopes",
	}
	cmd.AddCommand(newKnowledgeScopeAddCmd())
	return cmd
}

func newKnowledgeScopeAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <name>...",
		Short: "Create truths/<name>/ so that project's sessions are extracted",
		Long: "Creates truths/<name>/ under the store the extractor writes, and commits and " +
			"pushes it. Extraction is gated on that directory: a session whose scope has none " +
			"is skipped, and there is deliberately no default scope. `loom status` lists the " +
			"scopes this host's sessions resolve to that have no directory yet. See " +
			"docs/knowledge-scopes.md.",
		Args: cobra.MinimumNArgs(1),
		// A refused name is one line on stderr, printed once by Execute, rather
		// than cobra's copy plus a usage dump.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return addKnowledgeScopes(cmd.OutOrStdout(), args)
		},
	}
}

// addKnowledgeScopes creates one directory per named scope, as one record.
//
// The store is the extractor's, resolved from its persisted tunables rather
// than from knowledge.Root(): the gate a sweep checks is under that root, and a
// scope created anywhere else would leave the sessions it was created for still
// skipped.
func addKnowledgeScopes(out io.Writer, names []string) error {
	// Validated whole before anything is written, as a write plan is: a
	// half-applied invocation leaves the operator to work out which half. The
	// name half of the gate only — the directory this is about to create is
	// precisely what the store half would refuse the name for.
	for _, name := range names {
		if err := extract.ValidScopeName(name); err != nil {
			return err
		}
	}
	root := extract.CurrentSettings().KnowledgeRoot
	// Ahead of the store's own open, which reports the ENOENT of a path the
	// operator never named: a store is a git repo with a SCHEMA.md that nothing
	// here produces, so a machine without one is a misconfiguration to state.
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return fmt.Errorf("no knowledge store at %s — nothing to add a scope to", root)
	}
	// The sweep's own gate path, not a join of our own, so the directory this
	// creates is the one the sweep checks.
	truths := extract.TruthsDir()

	var created []string
	seen := map[string]bool{}
	for _, name := range names {
		// A name repeated in argv is one scope: no stat finds it yet, so without
		// this it would reach the subject, the write and the report twice over.
		if seen[name] {
			continue
		}
		seen[name] = true
		// A scope that already exists is what was asked for, so it is reported
		// and not an error: onboarding several projects at once must not fail on
		// the one already done.
		if fi, err := os.Stat(filepath.Join(truths, name)); err == nil && fi.IsDir() {
			fmt.Fprintf(out, "scope %s already exists\n", name)
			continue
		}
		created = append(created, name)
	}
	if len(created) == 0 {
		return nil
	}

	// One record for the whole invocation; the store flattens and bounds the
	// subject (store.SanitizeRecord) before it becomes a commit message.
	message := "add knowledge scope " + strings.Join(created, ", ")
	warn, err := store.ApplyIn(root, message, func(tx *store.Tx) error {
		for _, name := range created {
			if err := tx.WriteFile(filepath.Join(truths, name, gitkeepName), nil); err != nil {
				return err
			}
		}
		return nil
	})
	for _, name := range created {
		// Re-stat rather than assume: a refused write leaves the names after it
		// uncreated, and the store commits what landed before it either way. The
		// same check the loop above makes, since a name already taken by a regular
		// file is a path where nothing was created however the write ended.
		if fi, statErr := os.Stat(filepath.Join(truths, name)); statErr == nil && fi.IsDir() {
			fmt.Fprintf(out, "created %s\n", filepath.Join(truths, name))
		}
	}
	if warn.NotCommitted != "" {
		fmt.Fprintf(out, "not committed: %s\n", warn.NotCommitted)
	}
	if warn.NotPushed != "" {
		fmt.Fprintf(out, "not pushed: %s\n", warn.NotPushed)
	}
	return err
}

// validatePlan rejects a plan whose ops or required fields the store could not
// act on.
func validatePlan(plan writePlan) error {
	if plan.Message == "" {
		return fmt.Errorf("plan: message is required")
	}
	if len(plan.Changes) == 0 {
		return fmt.Errorf("plan: changes is empty")
	}
	for i, c := range plan.Changes {
		// Only a rename's destination has a record elsewhere to be dropped for.
		// Refused rather than ignored: a writer that believed it had exempted a
		// path would meet the exemption's absence as a commit failed by an ignored
		// path, which names neither the plan nor the field.
		if c.Droppable && c.Op != opRename {
			return fmt.Errorf("changes[%d]: droppable applies to rename only", i)
		}
		switch c.Op {
		case opWrite, opAppend, opRemove, opTouch:
			if c.Path == "" {
				return fmt.Errorf("changes[%d]: %s requires path", i, c.Op)
			}
		case opRename:
			if c.From == "" || c.To == "" {
				return fmt.Errorf("changes[%d]: rename requires from and to", i)
			}
		default:
			return fmt.Errorf("changes[%d]: unknown op %q", i, c.Op)
		}
	}
	return nil
}

// applyChanges performs a validated plan's changes in order, stopping at the
// first failure — the changes that landed before it are still committed, since
// Apply records what the closure touched however it ended.
func applyChanges(tx *store.Tx, changes []change) error {
	for _, c := range changes {
		var err error
		switch c.Op {
		case opWrite:
			err = tx.WriteFile(c.Path, []byte(c.Body))
		case opAppend:
			err = tx.Append(c.Path, c.Text)
		case opRemove:
			err = tx.Remove(c.Path)
		case opRename:
			err = tx.Rename(c.From, c.To)
			// Only a rename that happened has a destination to declare droppable.
			if err == nil && c.Droppable {
				tx.Droppable(c.To)
			}
		case opTouch:
			err = tx.Touch(c.Path)
		default:
			// Unreachable through validatePlan, which refuses an unknown op. Here
			// so that an op added to the vocabulary and to validation but not to
			// this switch fails the plan rather than being silently skipped and
			// reported as applied.
			err = fmt.Errorf("unhandled op %q", c.Op)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func init() {
	rootCmd.AddCommand(knowledgeCmd)
}
