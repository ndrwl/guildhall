package cli

import (
	"strings"
	"testing"
)

func TestStepSendBackHelpAndValidation(t *testing.T) {
	d := newRunDirector(t)
	code, out, stderr := d.run(t, "step", "send-back", "--help")
	if code != 0 || !strings.Contains(out, "--feedback") || stderr != "" {
		t.Fatalf("help = %d %q %q", code, out, stderr)
	}
	for _, args := range [][]string{
		{"--unknown", "x"},
		{"--task", "t-1", "--step", "plan", "--attempt", "1"},
		{"--task", "t-1", "--step", "plan", "--attempt", "0", "--feedback", "again"},
		{"--task", "t-1", "--step", "plan", "--attempt", "1", "--feedback", "again", "extra"},
	} {
		if code, _, _ := d.run(t, append([]string{"step", "send-back"}, d.flags(args...)...)...); code != 2 {
			t.Errorf("%v exit %d", args, code)
		}
	}
	if _, out, _ := d.run(t, "help"); !strings.Contains(out, "step send-back") {
		t.Fatalf("help omits send-back: %s", out)
	}
}

func TestStepSendBackLaunchesNewAttemptWithFeedback(t *testing.T) {
	d := newRunDirector(t)
	if code, _, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 {
		t.Fatal(stderr)
	}
	d.waitResult(t, "plan", 1)
	if code, _, stderr := d.run(t, append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--result", d.result("plan", 1))...)...); code != 0 {
		t.Fatal(stderr)
	}
	args := append([]string{"step", "send-back"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--feedback", "Clarify the plan before proceeding.")...)
	for i := range 2 {
		code, out, stderr := d.run(t, args...)
		if code != 0 || (i == 0 && !strings.Contains(out, `"step":"plan","attempt":2`)) {
			t.Fatalf("send-back = %d %q %s", code, out, stderr)
		}
	}
	d.waitResult(t, "plan", 2)
	if got := d.attempts(t); len(got) != 2 {
		t.Fatalf("attempts = %+v", got)
	}
}
