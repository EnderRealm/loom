package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

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
