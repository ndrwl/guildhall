package testenv_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTimingPolicy is also an explicit CI check. This is a source-policy
// checker, not a source-text proxy for a product behavior test.
func TestTimingPolicy(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		isTest := strings.HasSuffix(path, "_test.go") || strings.HasSuffix(filepath.Base(filepath.Dir(path)), "test")
		if !isTest {
			f, err := parser.ParseFile(token.NewFileSet(), path, b, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				if imp.Path.Value == `"testing"` {
					isTest = true
				}
			}
		}
		if !isTest {
			return nil
		}
		for _, issue := range timingIssues(path, string(b)) {
			t.Error(issue)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTimingPolicyRules(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"short ceiling", `func f() { <-time.After(2*time.Second) }`, 1},
		{"generous ceiling", `func f() { <-time.After(30*time.Second) }`, 0},
		{"scaled short ceiling", `func f() { <-time.After(testenv.Ceiling(time.Second)) }`, 1},
		{"named ceiling", `const budget = 200*time.Millisecond; func f() { _, _ = context.WithTimeout(ctx, budget) }`, 1},
		{"local ceiling", `func f() { budget := time.Second; _, _ = context.WithTimeout(ctx, budget) }`, 1},
		{"sleep synchronization", `func f() { time.Sleep(time.Second) }`, 1},
		{"poll cadence", `func f() { for end := time.Now().Add(time.Minute); time.Now().Before(end); time.Sleep(time.Millisecond) {} }`, 0},
		{"sleep exception", "func f() { time.Sleep(time.Second) // timing:allow intentional hung child, parent kills it.\n}", 0},
		{"empty exception", "func f() { time.Sleep(time.Second) // timing:allow\n}", 1},
		{"deadline", `func f() { _ = c.SetDeadline(time.Now().Add(time.Second)) }`, 1},
		{"options", `func f() { _ = Options{LockTimeout: time.Millisecond} }`, 1},
		{"cli timeout", `func f() { run("--timeout", "500ms") }`, 1},
		{"negative usage input", `func f() { run("--timeout", "-1s") }`, 0},
		{"elapsed assertion", `func f() { if time.Since(start) > time.Second {} }`, 1},
		{"assigned field", `func f() { app.StopGrace = time.Second }`, 1},
		{"local elapsed", `func f() { elapsed := time.Since(start); if elapsed > time.Second {} }`, 1},
		{"argv timeout", `func f() { _ = []string{"--timeout", "500ms"} }`, 1},
		{"attached CLI timeout", `func f() { run("--timeout=500ms") }`, 1},
		{"synthetic clock", `func f(t *testing.T) { synctest.Test(t, func(t *testing.T) { time.Sleep(time.Second); <-time.After(time.Millisecond) }) }`, 0},
		{"aliased time", `func f() { <-clock.After(clock.Second) }`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\nimport (\"time\"; clock \"time\"; \"context\"; \"testing/synctest\")\n" + tc.body
			if got := timingIssues("fixture.go", src); len(got) != tc.want {
				t.Fatalf("issues %v, want %d", got, tc.want)
			}
		})
	}
}

// The guard deliberately checks time sinks, not every duration literal:
// synthetic timestamps, polling intervals and parser fixtures are not budgets.
// It resolves file/local constants and Ceiling wrappers, including import aliases.
// Dynamic budgets still require review; this is not a whole-program dataflow proof.
func timingIssues(path, src string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return []string{err.Error()}
	}
	imports := map[string]string{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imports[name] = p
	}
	selector := func(e ast.Expr, pkg, name string) bool {
		s, ok := e.(*ast.SelectorExpr)
		if !ok || s.Sel.Name != name {
			return false
		}
		i, ok := s.X.(*ast.Ident)
		return ok && imports[i.Name] == pkg
	}
	var duration func(ast.Expr, int) (time.Duration, bool)
	duration = func(e ast.Expr, depth int) (time.Duration, bool) {
		if depth > 12 {
			return 0, false
		}
		switch e := e.(type) {
		case *ast.ParenExpr:
			return duration(e.X, depth+1)
		case *ast.BasicLit:
			if e.Kind == token.INT {
				v, err := strconv.ParseInt(e.Value, 0, 64)
				return time.Duration(v), err == nil
			}
		case *ast.SelectorExpr:
			for n, d := range map[string]time.Duration{"Nanosecond": time.Nanosecond, "Microsecond": time.Microsecond, "Millisecond": time.Millisecond, "Second": time.Second, "Minute": time.Minute, "Hour": time.Hour} {
				if selector(e, "time", n) {
					return d, true
				}
			}
		case *ast.BinaryExpr:
			a, ok := duration(e.X, depth+1)
			b, ok2 := duration(e.Y, depth+1)
			if !ok || !ok2 {
				return 0, false
			}
			switch e.Op {
			case token.MUL:
				return a * b, true
			case token.ADD:
				return a + b, true
			case token.SUB:
				return a - b, true
			case token.QUO:
				if b != 0 {
					return a / b, true
				}
			}
		case *ast.CallExpr:
			if s, ok := e.Fun.(*ast.SelectorExpr); ok && len(e.Args) == 1 && (s.Sel.Name == "Ceiling" || selector(e.Fun, "time", "Duration")) {
				return duration(e.Args[0], depth+1)
			}
		case *ast.Ident:
			if e.Obj == nil {
				return 0, false
			}
			switch d := e.Obj.Decl.(type) {
			case *ast.ValueSpec:
				for i, n := range d.Names {
					if n.Name == e.Name && i < len(d.Values) {
						return duration(d.Values[i], depth+1)
					}
				}
			case *ast.AssignStmt:
				for i, n := range d.Lhs {
					if n, ok := n.(*ast.Ident); ok && n.Name == e.Name && i < len(d.Rhs) {
						return duration(d.Rhs[i], depth+1)
					}
				}
			}
		}
		return 0, false
	}
	allowLines := map[int]bool{}
	for _, group := range f.Comments {
		for _, c := range group.List {
			if reason, ok := strings.CutPrefix(c.Text, "// timing:allow "); ok && len(strings.TrimSpace(reason)) >= 12 {
				allowLines[fset.Position(c.Pos()).Line] = true
			}
		}
	}
	allowed := func(n ast.Node) bool {
		line := fset.Position(n.Pos()).Line
		return allowLines[line] || allowLines[line-1]
	}
	var issues []string
	report := func(n ast.Node, why string) {
		if !allowed(n) {
			issues = append(issues, fmt.Sprintf("%s: %s; use event synchronization, a >=10s ceiling, or // timing:allow <reason>", fset.Position(n.Pos()), why))
		}
	}
	check := func(n ast.Node, e ast.Expr) {
		if d, ok := duration(e, 0); ok && d > 0 && d < 10*time.Second {
			report(n, "tight real-time limit "+d.String())
		}
	}
	limitName := func(name string) bool {
		switch name {
		case "Timeout", "LockTimeout", "StartTimeout", "ShellReady", "ResultGrace", "ResultTimeout", "StopGrace", "LockWait", "Cooperative", "Interrupt", "Terminate", "Kill":
			return true
		}
		return false
	}
	checkArgs := func(args []ast.Expr) {
		for i, arg := range args {
			lit, ok := arg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			flag, _ := strconv.Unquote(lit.Value)
			value := lit
			text, attached := strings.CutPrefix(flag, "--timeout=")
			if !attached {
				if flag != "--timeout" || i+1 >= len(args) {
					continue
				}
				value, ok = args[i+1].(*ast.BasicLit)
				if !ok || value.Kind != token.STRING {
					continue
				}
				text, _ = strconv.Unquote(value.Value)
			}
			d, err := time.ParseDuration(text)
			if err == nil && d > 0 && d < 10*time.Second {
				report(value, "tight CLI timeout "+text)
			}
		}
	}
	elapsed := func(e ast.Expr) bool {
		if c, ok := e.(*ast.CallExpr); ok {
			return selector(c.Fun, "time", "Since")
		}
		if i, ok := e.(*ast.Ident); ok && i.Obj != nil {
			if a, ok := i.Obj.Decl.(*ast.AssignStmt); ok {
				for n, lhs := range a.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == i.Name && n < len(a.Rhs) {
						c, ok := a.Rhs[n].(*ast.CallExpr)
						return ok && selector(c.Fun, "time", "Since")
					}
				}
			}
		}
		return false
	}
	// A sleep in a loop's post clause paces condition checks; it does not
	// assert that a single sleep makes another goroutine/process ready.
	polls := map[*ast.CallExpr]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if loop, ok := n.(*ast.ForStmt); ok && loop.Post != nil {
			ast.Inspect(loop.Post, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					polls[c] = true
				}
				return true
			})
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if selector(n.Fun, "testing/synctest", "Test") {
				return false
			} // Synthetic clock, not a real-time limit.
			if selector(n.Fun, "time", "Sleep") && !polls[n] {
				report(n, "sleep used outside a condition-poll loop")
			}
			for _, name := range []string{"After", "AfterFunc", "NewTimer"} {
				if selector(n.Fun, "time", name) && len(n.Args) > 0 {
					check(n, n.Args[0])
				}
			}
			if (selector(n.Fun, "context", "WithTimeout") || selector(n.Fun, "context", "WithTimeoutCause")) && len(n.Args) > 1 {
				check(n, n.Args[1])
			}
			if s, ok := n.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "Add" && len(n.Args) == 1 {
				if call, ok := s.X.(*ast.CallExpr); ok && selector(call.Fun, "time", "Now") {
					check(n, n.Args[0])
				}
			}
			checkArgs(n.Args)
		case *ast.CompositeLit:
			checkArgs(n.Elts)
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if field, ok := lhs.(*ast.SelectorExpr); ok && limitName(field.Sel.Name) && i < len(n.Rhs) {
					check(n, n.Rhs[i])
				}
			}
		case *ast.KeyValueExpr:
			if k, ok := n.Key.(*ast.Ident); ok && limitName(k.Name) {
				check(n, n.Value)
			}
		case *ast.BinaryExpr:
			if n.Op != token.LSS && n.Op != token.GTR && n.Op != token.LEQ && n.Op != token.GEQ {
				break
			}
			if elapsed(n.X) {
				check(n, n.Y)
			}
			if elapsed(n.Y) {
				check(n, n.X)
			}
		}
		return true
	})
	return issues
}
