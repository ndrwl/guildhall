package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestTaskListByWorkflow: task list --workflow lists only the tasks bound to that workflow name, in every
// repository unless --repo is given, and --open only the open ones, so the Director can see what still runs a
// workflow before its file is deleted.
func TestTaskListByWorkflow(t *testing.T) {
	path := emptyStore(t)
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ repo, id, workflow string }{
		{"app", "d1", "dogfood"}, {"app", "d2", "dogfood"}, {"app", "f1", "default"}, {"lib", "d3", "dogfood"},
	} {
		rev := workflow.Revision{Name: c.workflow, Digest: "sha256:" + strings.Repeat("a", 64)}
		if _, err := s.CreateTask("task:"+c.repo+":"+c.id, store.Task{Repository: c.repo, ID: c.id, Title: c.id, Workflow: rev, ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetTaskState("done-d2", "app", "d2", store.TaskDone); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want []string
	}{
		{nil, []string{"d1", "d2", "f1", "d3"}},
		{[]string{"--workflow", "dogfood"}, []string{"d1", "d2", "d3"}},
		{[]string{"--workflow", "dogfood", "--open"}, []string{"d1", "d3"}},
		{[]string{"--open", "--workflow", "dogfood", "--repo", "app"}, []string{"d1"}},
		{[]string{"--open"}, []string{"d1", "f1", "d3"}},
		{[]string{"--workflow", "gone"}, []string{}},
	} {
		code, out, stderr := runTask(t, append(append([]string{"list", "--store", path}, c.args...), "--json")...)
		if code != 0 {
			t.Fatalf("%v: exit %d, stderr %s", c.args, code, stderr)
		}
		got := []string{}
		for _, v := range decodeJSON[[]taskView](t, out) {
			got = append(got, v.ID)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%v: tasks %v, want %v", c.args, got, c.want)
		}
	}
	if code, _, stderr := runTask(t, "show", "--store", path, "--repo", "app", "--workflow", "dogfood", "--json", "d1"); code != 2 || !strings.Contains(stderr, "--workflow does not apply") {
		t.Fatalf("show --workflow: exit %d, stderr %s", code, stderr)
	}
}
