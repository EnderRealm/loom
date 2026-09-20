package pricing

import (
	"strings"
	"testing"
	"time"
)

const datedTable = `{
  "currency": "USD",
  "source": "test",
  "checked": "2026-09-11",
  "rates": [
    {"model": "m", "effective": "2026-03-01", "input": 4, "output": 20, "cache_write_5m": 5, "cache_write_1h": 8, "cache_read": 0.4},
    {"model": "m", "effective": "2026-01-01", "input": 5, "output": 25, "cache_write_5m": 6.25, "cache_write_1h": 10, "cache_read": 0.5}
  ]
}`

func TestLookupPicksTheRateInForce(t *testing.T) {
	tbl, err := Parse([]byte(datedTable))
	if err != nil {
		t.Fatal(err)
	}
	day := func(s string) time.Time {
		t.Helper()
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	// A run between the two effective dates prices at the earlier entry; one
	// on or after the later date at the later entry, regardless of the order
	// the table listed them in.
	if r, ok := tbl.Lookup("m", day("2026-02-15")); !ok || r.Input != 5 {
		t.Fatalf("Lookup(2026-02-15) = %+v, %v, want the 2026-01-01 rate", r, ok)
	}
	if r, ok := tbl.Lookup("m", day("2026-03-01")); !ok || r.Input != 4 {
		t.Fatalf("Lookup(2026-03-01) = %+v, %v, want the 2026-03-01 rate", r, ok)
	}
	if r, ok := tbl.Lookup("m", day("2026-09-11").Add(time.Hour)); !ok || r.Input != 4 {
		t.Fatalf("Lookup(2026-09-11) = %+v, %v, want the 2026-03-01 rate", r, ok)
	}
	// Before every effective date there is no rate: re-running the report
	// over an old window must not reprice history at today's rate.
	if _, ok := tbl.Lookup("m", day("2025-12-31")); ok {
		t.Fatal("Lookup(2025-12-31) found a rate, want none before the first effective date")
	}
	if _, ok := tbl.Lookup("other", day("2026-02-15")); ok {
		t.Fatal("Lookup(other) found a rate, want none for an unknown model")
	}
	if _, ok := tbl.Lookup("m", time.Time{}); ok {
		t.Fatal("Lookup(zero time) found a rate, want none")
	}
}

func TestPriceArithmetic(t *testing.T) {
	r := Rate{Model: "m", Input: 5, Output: 25, CacheWrite5m: 6.25, CacheWrite1h: 10, CacheRead: 0.5}
	// 1,000,000 input = 5; 100,000 output = 2.5; 200,000 5m writes = 1.25;
	// 100,000 1h writes = 1; 2,000,000 cache reads = 1.
	got, err := r.Price(Usage{Input: 1_000_000, Output: 100_000, CacheWrite5m: 200_000, CacheWrite1h: 100_000, CacheRead: 2_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if got != 10.75 {
		t.Fatalf("Price = %v, want 10.75", got)
	}
}

func TestPriceFastMode(t *testing.T) {
	fastIn, fastOut := 10.0, 50.0
	r := Rate{Model: "m", Input: 5, Output: 25, CacheWrite5m: 6.25, CacheWrite1h: 10, CacheRead: 0.5, FastInput: &fastIn, FastOutput: &fastOut}
	// Fast: 1,000,000 input = 10; 100,000 output = 5; 1,000,000 cache reads
	// at 0.1 × the fast input rate = 1.
	got, err := r.Price(Usage{Input: 1_000_000, Output: 100_000, CacheRead: 1_000_000, Fast: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != 16 {
		t.Fatalf("fast Price = %v, want 16", got)
	}

	standard := Rate{Model: "m", Input: 5, Output: 25}
	if _, err := standard.Price(Usage{Input: 1, Fast: true}); err == nil || !strings.Contains(err.Error(), `"m"`) {
		t.Fatalf("fast Price on a model without fast rates = %v, want an error naming the model", err)
	}
	unscalable := Rate{Model: "m", Input: 0, Output: 25, CacheRead: 0.5, FastInput: &fastIn, FastOutput: &fastOut}
	if _, err := unscalable.Price(Usage{Input: 1, Fast: true}); err == nil || !strings.Contains(err.Error(), `"m"`) {
		t.Fatalf("fast Price on a model without a standard input rate = %v, want an error naming the model", err)
	}
}

// TestPriceFastModeUsesTheEntrysOwnCacheRatios pins that fast cache rates
// scale the entry's own cache rates rather than assuming the usual
// multipliers: a model whose cache hit is 0.025× input prices fast cache
// reads at 0.025× the fast input rate.
func TestPriceFastModeUsesTheEntrysOwnCacheRatios(t *testing.T) {
	fastIn, fastOut := 20.0, 100.0
	r := Rate{Model: "m", Input: 10, Output: 50, CacheWrite5m: 12.5, CacheWrite1h: 20, CacheRead: 0.25, FastInput: &fastIn, FastOutput: &fastOut}
	// 1,000,000 fast cache reads at 0.025 × $20/M = 0.5.
	got, err := r.Price(Usage{CacheRead: 1_000_000, Fast: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != 0.5 {
		t.Fatalf("fast cache-read Price = %v, want 0.5", got)
	}
}

func TestParseRejectsAMalformedTable(t *testing.T) {
	cases := map[string]string{
		"not json": `{`,
		"no checked date": `{"currency":"USD","source":"s","rates":[
			{"model":"m","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1}]}`,
		"no model": `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1}]}`,
		"bad effective": `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"model":"m","effective":"Jan 2026","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1}]}`,
		"negative rate": `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"model":"m","effective":"2026-01-01","input":-1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1}]}`,
		"duplicate": `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"model":"m","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1},
			{"model":"m","effective":"2026-01-01","input":2,"output":2,"cache_write_5m":2,"cache_write_1h":2,"cache_read":2}]}`,
		"fast input alone": `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"model":"m","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1,"fast_input":2}]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("Parse accepted a malformed table")
			}
		})
	}
}

// TestParseRequiresEveryStandardRate pins that a missing or null rate is an
// error rather than a silent $0: an omitted or misspelled cache_read would
// otherwise zero out the bulk of a run's tokens without warning.
func TestParseRequiresEveryStandardRate(t *testing.T) {
	fields := []string{"input", "output", "cache_write_5m", "cache_write_1h", "cache_read"}
	table := func(omit, null string) string {
		var parts []string
		for _, f := range fields {
			switch f {
			case omit: // dropped from the entry
			case null:
				parts = append(parts, `"`+f+`":null`)
			default:
				parts = append(parts, `"`+f+`":1`)
			}
		}
		// Entry 0 is well-formed so the error must point at entry 1 (n).
		return `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"model":"m","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1},
			{"model":"n","effective":"2026-01-01",` + strings.Join(parts, ",") + `}]}`
	}
	for _, f := range fields {
		for _, c := range []struct{ kind, data string }{{"omitted", table(f, "")}, {"null", table("", f)}} {
			t.Run(f+" "+c.kind, func(t *testing.T) {
				_, err := Parse([]byte(c.data))
				if err == nil {
					t.Fatalf("Parse accepted a table with %s %s", f, c.kind)
				}
				msg := err.Error()
				if !strings.Contains(msg, f) || !strings.Contains(msg, "entry 1 (n)") {
					t.Fatalf("error = %q, want it to name entry 1 (n) and field %s", msg, f)
				}
			})
		}
	}

	zero := `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
		{"model":"free","effective":"2026-01-01","input":0,"output":0,"cache_write_5m":0,"cache_write_1h":0,"cache_read":0}]}`
	tbl, err := Parse([]byte(zero))
	if err != nil {
		t.Fatalf("Parse rejected explicit zero rates: %v", err)
	}
	if r, ok := tbl.Lookup("free", tbl.Checked); !ok || r.Input != 0 || r.CacheRead != 0 {
		t.Fatalf("Lookup(free) = %+v, %v, want an all-zero rate", r, ok)
	}
}

func TestDefaultTableLoadsAndNamesItsProvenance(t *testing.T) {
	tbl, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Currency != "USD" || tbl.Source == "" || tbl.Checked.IsZero() {
		t.Fatalf("table = %+v, want currency, source and checked date", tbl)
	}
	if _, ok := tbl.Lookup("claude-opus-5", tbl.Checked); !ok {
		t.Fatal("claude-opus-5 has no rate on the checked date")
	}
}

// TestDefaultTableCoversRecordedCodexAndCursorModels pins the identities Codex
// and Cursor transcripts record against the default table: each resolves on
// and after its effective date and not before, carries its own vendor
// provenance, and the identities no vendor page prices stay misses. The
// Claude entries are unchanged and still carry the table's provenance.
func TestDefaultTableCoversRecordedCodexAndCursorModels(t *testing.T) {
	tbl, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	day := func(s string) time.Time {
		t.Helper()
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	openai := "https://developers.openai.com/api/docs/pricing"
	cursor := "https://cursor.com/docs/models"
	checked := day("2026-09-20")
	for _, c := range []struct {
		model, at, source                     string
		input, output, write5m, write1h, read float64
	}{
		{"gpt-6-astra", "2026-09-03", openai, 10, 50, 12.5, 12.5, 1},
		{"gpt-6-astra", "2026-09-15", openai, 10, 50, 12.5, 12.5, 1},
		{"gpt-5.6-sol", "2026-08-21", openai, 4, 20, 5, 5, 0.4},
		{"gpt-5.4", "2026-03-05", openai, 2.5, 15, 0, 0, 0.25},
		{"gpt-5.3-codex", "2026-02-24", openai, 1.75, 14, 0, 0, 0.175},
		{"gpt-5.2-codex", "2026-01-14", "https://developers.openai.com/api/docs/models/gpt-5.2-codex", 1.75, 14, 0, 0, 0.175},
		{"composer-2.5-fast", "2026-01-01", cursor, 3, 15, 0, 0, 0.5},
		{"cursor-grok-4.5-high", "2026-01-01", cursor, 2, 6, 0, 0, 0.5},
		{"gpt-5.6-sol-high", "2026-08-21", cursor, 4, 20, 5, 5, 0.4},
		{"gpt-5.6-sol-xhigh", "2026-08-21", cursor, 4, 20, 5, 5, 0.4},
	} {
		r, ok := tbl.Lookup(c.model, day(c.at))
		if !ok {
			t.Errorf("Lookup(%s, %s) found no rate", c.model, c.at)
			continue
		}
		if r.Input != c.input || r.Output != c.output || r.CacheWrite5m != c.write5m || r.CacheWrite1h != c.write1h || r.CacheRead != c.read {
			t.Errorf("Lookup(%s, %s) = %+v, want %v/%v/%v/%v/%v", c.model, c.at, r, c.input, c.output, c.write5m, c.write1h, c.read)
		}
		if r.FastInput != nil || r.FastOutput != nil {
			t.Errorf("Lookup(%s, %s) carries fast rates %v/%v; rollouts record no service tier", c.model, c.at, r.FastInput, r.FastOutput)
		}
		if r.Source != c.source || !r.Checked.Equal(checked) {
			t.Errorf("Lookup(%s, %s) provenance = %s @ %s, want %s @ 2026-09-20", c.model, c.at, r.Source, r.Checked.Format(time.DateOnly), c.source)
		}
	}
	// The day before each effective date is a miss: a run then must not be
	// priced at a rate the vendor had not yet published.
	for _, c := range []struct{ model, at string }{
		{"gpt-6-astra", "2026-09-02"},
		{"gpt-5.6-sol", "2026-08-20"},
		{"gpt-5.4", "2026-03-04"},
		{"gpt-5.3-codex", "2026-02-23"},
		{"gpt-5.2-codex", "2026-01-13"},
		{"gpt-5.6-sol-high", "2026-08-20"},
		{"composer-2.5-fast", "2025-12-31"},
	} {
		if r, ok := tbl.Lookup(c.model, day(c.at)); ok {
			t.Errorf("Lookup(%s, %s) = %+v, want none before the effective date", c.model, c.at, r)
		}
	}
	// Recorded identities no vendor page prices: Codex's auto-review label,
	// the gpt-5.6 alias, and Cursor's Fable thinking-max variant, whose
	// Max Mode surcharge the Cursor page documents for legacy plans only.
	for _, model := range []string{"codex-auto-review", "gpt-5.6", "claude-fable-5-1-thinking-max"} {
		if r, ok := tbl.Lookup(model, checked); ok {
			t.Errorf("Lookup(%s) = %+v, want none: no vendor page prices that identity", model, r)
		}
	}
	r, ok := tbl.Lookup("claude-opus-5", checked)
	if !ok || r.Input != 5 || r.Output != 25 || r.CacheWrite5m != 6.25 || r.CacheWrite1h != 10 || r.CacheRead != 0.5 || r.FastInput == nil || *r.FastInput != 10 || *r.FastOutput != 50 {
		t.Errorf("claude-opus-5 = %+v, want the unchanged 5/25/6.25/10/0.5 fast 10/50", r)
	}
	if r.Source != tbl.Source || !r.Checked.Equal(tbl.Checked) {
		t.Errorf("claude-opus-5 provenance = %s @ %s, want the table's %s @ %s", r.Source, r.Checked.Format(time.DateOnly), tbl.Source, tbl.Checked.Format(time.DateOnly))
	}
}

// TestPriceOpenAIEntry pins the arithmetic for a Codex unit whose caller has
// already taken the cache read back out of the input: the read prices at
// the cached-input rate and nothing else is charged for it.
func TestPriceOpenAIEntry(t *testing.T) {
	tbl, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	r, ok := tbl.Lookup("gpt-6-astra", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("gpt-6-astra has no rate on 2026-09-15")
	}
	// 400,000 billable input at $10/M = 4; 600,000 cached input at $1/M =
	// 0.6; 100,000 output at $50/M = 5.
	got, err := r.Price(Usage{Input: 400_000, CacheRead: 600_000, Output: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	if got != 9.6 {
		t.Fatalf("Price = %v, want 9.6", got)
	}
}

// TestParseEntryProvenance pins that an entry's own source and checked date
// are read when given, default to the table's when not, and that a
// malformed per-entry checked date is rejected rather than defaulted.
func TestParseEntryProvenance(t *testing.T) {
	tbl, err := Parse([]byte(`{"currency":"USD","source":"table-page","checked":"2026-09-11","rates":[
		{"model":"own","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1,"source":"own-page","checked":"2026-09-20","note":"free text"},
		{"model":"inherited","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	own, _ := tbl.Lookup("own", tbl.Checked)
	if own.Source != "own-page" || own.Checked.Format(time.DateOnly) != "2026-09-20" {
		t.Errorf("own entry provenance = %s @ %s, want own-page @ 2026-09-20", own.Source, own.Checked.Format(time.DateOnly))
	}
	inherited, _ := tbl.Lookup("inherited", tbl.Checked)
	if inherited.Source != "table-page" || !inherited.Checked.Equal(tbl.Checked) {
		t.Errorf("inherited entry provenance = %s @ %s, want table-page @ 2026-09-11", inherited.Source, inherited.Checked.Format(time.DateOnly))
	}

	_, err = Parse([]byte(`{"currency":"USD","source":"table-page","checked":"2026-09-11","rates":[
		{"model":"m","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1,"checked":"Sep 2026"}]}`))
	if err == nil || !strings.Contains(err.Error(), "entry 0 (m)") || !strings.Contains(err.Error(), `checked "Sep 2026"`) {
		t.Fatalf("Parse accepted a malformed per-entry checked date: %v", err)
	}
}
