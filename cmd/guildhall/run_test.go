package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestTaskRunsEndToEnd is the S7 acceptance: the built binary runs task app/t-1 on the fake-straight workflow
// (internal/cli/testdata/run) from task new to its terminal step, the merge decision, through the real commands:
// task start launches each Artisan stage as its own owned launcher process, the fake Artisan writes its result,
// step done takes it, decide answer and task start --decision cross each Guildmaster step. Afterwards every
// attempt's launcher is proven stopped and the timeline records the run in order.
func TestTaskRunsEndToEnd(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "guildhall")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command(gobin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cfg, err := filepath.Abs(filepath.Join("..", "..", "internal", "cli", "testdata", "run", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.MkdirTemp("", "ge")
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "director.store")
	s, err := store.Open(storePath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	try := func(stdin string, args ...string) (string, string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Stdin = strings.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	run := func(stdin string, args ...string) string {
		t.Helper()
		out, stderr, err := try(stdin, args...)
		if err != nil {
			t.Fatalf("guildhall %s: %v\nstdout %s\nstderr %s", strings.Join(args, " "), err, out, stderr)
		}
		return out
	}
	attempts := []string{}
	// stopAll proves every attempt's launcher stopped, so no owned process outlives the test; a launcher
	// that has just recorded its outcome may still be exiting, and stop proves it only once it is gone.
	stopAll := func() {
		t.Helper()
		for _, a := range attempts {
			step, n, _ := strings.Cut(a, "/")
			for end := time.Now().Add(time.Minute); ; time.Sleep(50 * time.Millisecond) {
				out, stderr, err := try("", "artisan", "stop", "--store", storePath, "--state", state, "--repo", "app", "--task", "t-1", "--step", step, "--attempt", n)
				if err == nil {
					break
				}
				if time.Now().After(end) {
					t.Errorf("artisan stop %s: %v\n%s%s", a, err, out, stderr)
					break
				}
			}
		}
	}
	t.Cleanup(func() {
		stopAll()
		_ = os.RemoveAll(state)
	})
	flags := []string{"--store", storePath, "--config", cfg, "--state", state, "--repo", "app"}
	want := func(got, want string) {
		t.Helper()
		if strings.TrimSpace(got) != want {
			t.Fatalf("got %s, want %s", strings.TrimSpace(got), want)
		}
	}
	// done waits for the attempt's result and takes it.
	done := func(step string, n int) string {
		t.Helper()
		attempts = append(attempts, step+"/"+strconv.Itoa(n))
		k := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: step, Attempt: n}
		result := filepath.Join(k.Dir(state), artisan.ResultFile)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := adapter.WaitResult(ctx, result, adapter.Identity{TaskID: "t-1", AttemptID: k.AttemptID()}, adapter.WaitOptions{Poll: 50 * time.Millisecond}); err != nil {
			log, _ := os.ReadFile(filepath.Join(k.Dir(state), "launcher.log"))
			t.Fatalf("result of %s: %v\nlauncher log:\n%s", k, err, log)
		}
		return run("", append(append([]string{"step", "done"}, flags...), "--task", "t-1", "--step", step, "--attempt", strconv.Itoa(n), "--result", result)...)
	}
	answer := func(decision string) string {
		t.Helper()
		run("approved\nLooks right.\n", "decide", "answer", "--store", storePath, "--repo", "app", "--task", "t-1", "--decision", decision, "--answer-file", "-")
		return run("", append(append([]string{"task", "start"}, flags...), "--decision", decision, "t-1")...)
	}

	// fake-straight has no validate step, so it is below the review floor and task new needs its approval.
	run("Approved for the acceptance run.\n", "workflow", "approve", "--store", storePath, "--config", cfg, "--repo", "app", "--words-file", "-")
	run("", "task", "new", "--store", storePath, "--config", cfg, "--repo", "app", "--title", "Sort the task list", "t-1")
	want(run("", append(append([]string{"task", "start"}, flags...), "t-1")...), `{"wait":"attempt","step":"plan","attempt":1}`)
	want(done("plan", 1), `{"wait":"decision","step":"accept","decision":"accept-1"}`)
	want(answer("accept-1"), `{"wait":"attempt","step":"build","attempt":1}`)
	want(done("build", 1), `{"wait":"decision","step":"sign-off","decision":"sign-off-1"}`)
	want(answer("sign-off-1"), `{"wait":"decision","step":"deliver","decision":"deliver-1"}`)

	stopAll()

	var tl struct {
		Attempts []struct {
			Step    string `json:"step"`
			Number  int    `json:"number"`
			Outcome string `json:"outcome"`
		} `json:"attempts"`
		Decisions []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Answered bool   `json:"answered"`
			Choice   string `json:"choice"`
		} `json:"decisions"`
		Events []store.Receipt `json:"events"`
	}
	if err := json.Unmarshal([]byte(run("", "task", "timeline", "--store", storePath, "--repo", "app", "t-1", "--json")), &tl); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range tl.Attempts {
		got = append(got, a.Step+"-"+strconv.Itoa(a.Number)+" "+a.Outcome)
	}
	for _, d := range tl.Decisions {
		got = append(got, d.ID+" "+d.Kind+" "+strconv.FormatBool(d.Answered)+" "+d.Choice)
	}
	if exp := []string{"plan-1 succeeded", "build-1 succeeded", "accept-1 approval true approved", "sign-off-1 review true approved", "deliver-1 merge false "}; !slices.Equal(got, exp) {
		t.Errorf("timeline records:\n%q\nwant\n%q", got, exp)
	}
	var order []string
	for _, e := range tl.Events {
		switch e.Operation {
		case "create-task", "start-attempt", "finish-attempt", "open-decision", "answer-decision":
			order = append(order, e.Operation+" "+e.Record)
		}
	}
	exp := []string{
		"create-task task",
		"start-attempt attempt/plan/1", "finish-attempt attempt/plan/1", "open-decision decision/accept-1", "answer-decision decision/accept-1",
		"start-attempt attempt/build/1", "finish-attempt attempt/build/1", "open-decision decision/sign-off-1", "answer-decision decision/sign-off-1",
		"open-decision decision/deliver-1",
	}
	if !slices.Equal(order, exp) {
		t.Errorf("timeline events:\n%q\nwant\n%q", order, exp)
	}
}
