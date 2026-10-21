# Good and bad Go tests

## Good: a table test through the public interface

The expected values are literals from the contract, and each case names a behaviour, not a step.

```go
func TestParseDuration(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     time.Duration
		err      error
	}{
		{"minutes", "5m", 5 * time.Minute, nil},
		{"empty is an error", "", 0, config.ErrEmpty},
		{"negative is refused", "-1s", 0, config.ErrNegative},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := config.ParseDuration(c.in)
			if !errors.Is(err, c.err) || got != c.want {
				t.Errorf("ParseDuration(%q) = %v, %v; want %v, %v", c.in, got, err, c.want, c.err)
			}
		})
	}
}
```

## Bad: implementation-coupled and tautological

```go
// BAD: checks a private field and recomputes the answer the way the code does.
func TestQueueAdd(t *testing.T) {
	q := newQueue()
	q.Add(job)
	if len(q.items) != 1 { // unexported state, not the contract
		t.Fatal("not stored")
	}
	if q.Next().Due != job.Created.Add(q.delay) { // passes by construction
		t.Fatal("wrong due time")
	}
}

// GOOD: observes the stored job through the interface, against a literal.
func TestQueueAddMakesJobNext(t *testing.T) {
	q := queue.New(queue.Delay(time.Minute))
	q.Add(queue.Job{ID: "a", Created: t0})
	if got := q.Next(); got.ID != "a" || !got.Due.Equal(t0.Add(time.Minute)) {
		t.Fatalf("Next = %+v, want job a due one minute after creation", got)
	}
}
```

## A shared contract suite

One unexported function holds the cases, and each adapter gets a short test that hands it a constructor.
The double is trusted only because it passes the same cases as the real adapter.

```go
// testMailbox is the Mailbox contract; every adapter must pass it.
func testMailbox(t *testing.T, newMailbox func(t *testing.T) Mailbox) {
	t.Run("read returns what was sent", func(t *testing.T) {
		m := newMailbox(t)
		if err := m.Send("a", "hello"); err != nil {
			t.Fatal(err)
		}
		if got, err := m.Read("a"); err != nil || got != "hello" {
			t.Errorf("Read = %q, %v; want hello", got, err)
		}
	})
	t.Run("unknown recipient is ErrNoRecipient", func(t *testing.T) {
		if err := newMailbox(t).Send("", "x"); !errors.Is(err, ErrNoRecipient) {
			t.Errorf("Send error = %v, want ErrNoRecipient", err)
		}
	})
}

func TestStoreMailbox(t *testing.T) {
	testMailbox(t, func(t *testing.T) Mailbox { return openStore(t, t.TempDir()) })
}

func TestFakeMailbox(t *testing.T) {
	testMailbox(t, func(*testing.T) Mailbox { return NewFake() })
}
```

## A real process: the test binary as the program

To test a process boundary without installed tools, the test binary plays the external program when an environment variable names its role.
The test crosses a real process boundary on every platform and needs no network.

```go
const roleEnv = "EXAMPLE_TEST_ROLE"

func TestMain(m *testing.M) {
	if os.Getenv(roleEnv) == "tool" {
		fmt.Println("tool 1.0")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestVersionReadsTheTool(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(roleEnv, "tool")
	got, err := runner.Version(context.Background(), self)
	if err != nil || got != "1.0" {
		t.Fatalf("Version = %q, %v; want 1.0", got, err)
	}
}
```
