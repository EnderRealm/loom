package shipper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"loom/internal/config"
	"loom/transport/cursor"
	"loom/transport/internal/notify"
	"loom/transport/internal/source"
	"loom/transport/internal/staging"
	"loom/transport/internal/wire"
)

type registryAdapter struct{ project string }

func (registryAdapter) Agent() string { return source.ExecutionsAgent }
func (a registryAdapter) List() ([]source.Session, error) {
	return []source.Session{{Project: a.project, SessionID: "executions", Path: filepath.Join(config.Home(), "executions.jsonl")}}, nil
}
func putRegistry(t *testing.T, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(config.Home(), "executions.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func registryOffsets(t *testing.T, sourceWant, shipWant int64) {
	t.Helper()
	for kind, want := range map[cursor.Kind]int64{cursor.KindSource: sourceWant, cursor.KindShip: shipWant} {
		got, err := cursor.Read(kind, source.ExecutionsAgent, "executions")
		if err != nil || got != want {
			t.Errorf("%s cursor = %d, %v; want %d", kind, got, err, want)
		}
	}
}

// Discovery changes its hostname-derived project between capture passes.
// The HTTP peer checks byte offsets and keeps each destination separately.
func TestExecutionRegistryHostnameChange(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	received := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req wire.IngestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		prior := received[req.Project]
		if req.FromOffset != int64(len(prior)) {
			t.Errorf("%s from=%d, received=%d", req.Project, req.FromOffset, len(prior))
			w.WriteHeader(400)
			return
		}
		data := []byte("")
		for _, line := range req.Lines {
			data = append(data, []byte(line+"\n")...)
		}
		if req.ToOffset != req.FromOffset+int64(len(data)) {
			t.Error("offset/bytes mismatch")
			w.WriteHeader(400)
			return
		}
		received[req.Project] = append(prior, data...)
		json.NewEncoder(w).Encode(wire.IngestResponse{AcceptedToOffset: req.ToOffset})
	}))
	defer srv.Close()
	first := []byte("{\"record\":\"one\"}\n")
	all := append(append([]byte{}, first...), []byte("{\"record\":\"two\"}\n")...)
	for i, data := range [][]byte{first, all, all} {
		putRegistry(t, data)
		project := "studio_lan"
		if i > 0 {
			project = "studio_local"
		}
		counts := tickCounts{}
		captureAdapters(&counts, []source.Adapter{registryAdapter{project}})
		shipPass(&Config{ServerURL: srv.URL}, &counts)
		if counts.captureFailed != 0 || counts.failed != 0 {
			t.Fatalf("counts: %+v", counts)
		}
		registryOffsets(t, int64(len(data)), int64(len(data)))
	}
	if len(received) != 1 || !bytes.Equal(received["studio_lan"], all) {
		t.Fatalf("received=%q, want one destination containing %q exactly once", received, all)
	}
}

func TestExecutionRegistryStrandedPending(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	first := []byte("{\"record\":\"one\"}\n")
	delta := []byte("{}\n")
	putRegistry(t, append(append([]byte{}, first...), delta...))
	if err := staging.Append(source.ExecutionsAgent, "studio_lan", "", "executions", first); err != nil {
		t.Fatal(err)
	}
	if err := staging.Append(source.ExecutionsAgent, "studio_local", "", "executions", delta); err != nil {
		t.Fatal(err)
	}
	if err := cursor.Write(cursor.KindSource, source.ExecutionsAgent, "executions", int64(len(first)+len(delta))); err != nil {
		t.Fatal(err)
	}
	if err := cursor.Write(cursor.KindShip, source.ExecutionsAgent, "executions", int64(len(first))); err != nil {
		t.Fatal(err)
	}
	state := &notify.State{}
	refreshPending(state)
	if !state.PendingSessions[notify.SessionKey(source.ExecutionsAgent, "executions")] {
		t.Fatal("stranded execution bytes reported as pending=0")
	}
}

func splitRegistry(t *testing.T) ([]byte, string) {
	t.Helper()
	t.Setenv("LOOM_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	first := []byte("{\"record\":\"one\"}\n")
	delta := []byte("{\"record\":\"two\"}\n")
	all := append(append([]byte{}, first...), delta...)
	putRegistry(t, all)
	for project, data := range map[string][]byte{"studio_lan": first, "studio_local": delta} {
		if err := staging.Append(source.ExecutionsAgent, project, "", registrySession, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := cursor.Write(cursor.KindSource, source.ExecutionsAgent, registrySession, int64(len(all))); err != nil {
		t.Fatal(err)
	}
	if err := cursor.Write(cursor.KindShip, source.ExecutionsAgent, registrySession, int64(len(first))); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, source.ExecutionsAgent, "studio_lan", "executions.jsonl")
	if err := atomicRegistryWrite(path, first); err != nil {
		t.Fatal(err)
	}
	if err := atomicRegistryWrite(strings.TrimSuffix(path, ".jsonl")+".offset", []byte(strconv.Itoa(len(first)))); err != nil {
		t.Fatal(err)
	}
	return all, root
}

func TestReconcileExecutionRegistry(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial-repair=%t", partial), func(t *testing.T) {
			all, root := splitRegistry(t)
			canonical := staging.Path(source.ExecutionsAgent, "studio_lan", "", registrySession)
			if partial {
				if err := atomicRegistryWrite(canonical, all); err != nil {
					t.Fatal(err)
				}
			}
			var health bytes.Buffer
			if err := PrintHealth(&health); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(health.String(), "unshipped sessions:   1") || !strings.Contains(health.String(), "17 captured bytes") {
				t.Fatalf("health: %s", health.String())
			}
			counts := tickCounts{}
			captureAdapters(&counts, []source.Adapter{registryAdapter{"studio_local"}})
			shipPass(&Config{ServerURL: "http://127.0.0.1:1"}, &counts)
			if counts.captureFailed != 1 || counts.failed != 1 {
				t.Fatalf("split state not refused: %+v", counts)
			}
			var out bytes.Buffer
			if err := ReconcileExecutions("studio_lan", root, &out); err != nil {
				t.Fatal(err)
			}
			if err := ReconcileExecutions("studio_lan", root, &out); err != nil {
				t.Fatalf("repeat repair: %v", err)
			}
			registryOffsets(t, int64(len(all)), 17)
			entries, err := staging.List(source.ExecutionsAgent)
			if err != nil || len(entries) != 1 || entries[0].Project != "studio_lan" {
				t.Fatalf("entries=%v err=%v", entries, err)
			}
			got, err := os.ReadFile(canonical)
			if err != nil || !bytes.Equal(got, all) {
				t.Fatalf("staging=%q err=%v", got, err)
			}
			backups, err := filepath.Glob(filepath.Join(config.TransportDir(), "executions-reconcile-*", "staging", "studio_local.jsonl"))
			if err != nil || len(backups) != 1 {
				t.Fatalf("preserved fragment=%v err=%v", backups, err)
			}
			got, err = os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(got, all[17:]) {
				t.Fatalf("backup=%q err=%v", got, err)
			}
			received := append([]byte{}, all[:17]...)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req wire.IngestRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if req.Project != "studio_lan" || req.FromOffset != int64(len(received)) {
					t.Errorf("request=%+v", req)
					w.WriteHeader(400)
					return
				}
				for _, line := range req.Lines {
					received = append(received, []byte(line+"\n")...)
				}
				json.NewEncoder(w).Encode(wire.IngestResponse{AcceptedToOffset: int64(len(received))})
			}))
			defer srv.Close()
			for range 2 {
				counts = tickCounts{}
				captureAdapters(&counts, []source.Adapter{registryAdapter{"third_hostname"}})
				shipPass(&Config{ServerURL: srv.URL}, &counts)
				if counts.captureFailed != 0 || counts.failed != 0 {
					t.Fatalf("counts=%+v", counts)
				}
			}
			registryOffsets(t, int64(len(all)), int64(len(all)))
			if !bytes.Equal(received, all) {
				t.Fatalf("received=%q want=%q", received, all)
			}
			state := &notify.State{}
			refreshPending(state)
			if len(state.PendingSessions) != 0 {
				t.Fatalf("pending=%v", state.PendingSessions)
			}
		})
	}
}

func TestReconcileExecutionRegistryRefusesMismatches(t *testing.T) {
	for _, damage := range []string{"receiver", "receiver-offset", "fragment", "alternate-receiver", "source-cursor", "binding", "lock"} {
		t.Run(damage, func(t *testing.T) {
			_, root := splitRegistry(t)
			var path string
			data := []byte("{\"foreign\":true}\n")
			switch damage {
			case "receiver":
				path = filepath.Join(root, source.ExecutionsAgent, "studio_lan", "executions.jsonl")
			case "receiver-offset":
				path = filepath.Join(root, source.ExecutionsAgent, "studio_lan", "executions.offset")
			case "fragment":
				path = staging.Path(source.ExecutionsAgent, "studio_local", "", registrySession)
			case "alternate-receiver":
				path = filepath.Join(root, source.ExecutionsAgent, "studio_local", "executions.jsonl")
			case "source-cursor":
				if err := cursor.Write(cursor.KindSource, source.ExecutionsAgent, registrySession, 999); err != nil {
					t.Fatal(err)
				}
			case "binding":
				path = registryProjectPath()
				data = []byte("elsewhere\n")
			case "lock":
				unlock, err := acquireLock()
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			}
			if path != "" {
				if err := atomicRegistryWrite(path, data); err != nil {
					t.Fatal(err)
				}
			}
			before := registryFiles(t, config.Home())
			if err := ReconcileExecutions("studio_lan", root, io.Discard); err == nil {
				t.Fatal("mismatch accepted")
			}
			after := registryFiles(t, config.Home())
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed files\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func registryFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == "shipper.lock" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
