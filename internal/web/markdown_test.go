package web

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestMarkdownRendersGFMTables(t *testing.T) {
	got := string(RenderMarkdown([]byte("# T\n\n| a | b |\n|---|:-:|\n| `x` | y |\n\n- one\n  - nested\n    1. deep\n\n```go\nfunc f() {}\n```\n\n> quoted\n")))
	for _, want := range []string{"<h1>T</h1>", "<table>", "<th>a</th>", `<td style="text-align:center">y</td>`, "<td><code>x</code></td>",
		"<li>one\n<ul>\n<li>nested\n<ol>\n<li>deep</li>", `<pre><code class="language-go">func f() {}`, "<blockquote>\n<p>quoted</p>"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered lacks %q:\n%s", want, got)
		}
	}
}

func TestMarkdownIsSafe(t *testing.T) {
	src := "<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\ninline <b onclick=x>b</b>\n\n" +
		"[a](javascript:alert(1)) [b](vbscript:msgbox) [c](file:///etc/passwd) [d](JAVASCRIPT:alert(2)) ![e](javascript:alert(3))\n\n" +
		"[ok](https://example.com/x)\n"
	got := string(RenderMarkdown([]byte(src)))
	for _, bad := range []string{"<script", "<img src=\"x\"", "onerror", "onclick", "javascript:", "JAVASCRIPT:", "vbscript:", "file:"} {
		if strings.Contains(got, bad) {
			t.Errorf("rendered holds %q:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, `<a href="https://example.com/x">ok</a>`) {
		t.Errorf("a safe link is lost:\n%s", got)
	}
}

// unsafeMarkup reports a tag in rendered HTML that is a script, or an attribute named on…, scanning only the markup
// goldmark wrote, since it escapes every < of the input's text.
func unsafeMarkup(html string) string {
	for i := 0; i < len(html); i++ {
		if html[i] != '<' {
			continue
		}
		end := strings.IndexByte(html[i:], '>')
		if end < 0 {
			return ""
		}
		tag := html[i+1 : i+end]
		i += end
		if strings.HasPrefix(strings.ToLower(tag), "script") {
			return "<" + tag + ">"
		}
		quote := byte(0)
		for j := 0; j < len(tag); j++ {
			c := tag[j]
			switch {
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case (c == ' ' || c == '\n' || c == '\t') && j+3 < len(tag) && strings.EqualFold(tag[j+1:j+3], "on"):
				name := tag[j+1:]
				if k := strings.IndexAny(name, "= >"); k > 0 && name[k] == '=' {
					return "<" + tag + ">"
				}
			}
		}
	}
	return ""
}

func FuzzMarkdownIsSafe(f *testing.F) {
	for _, seed := range []string{"<script>x</script>", "<img src=x onerror=y>", "<div onclick=\"a\">", "[a](javascript:x)",
		"| <b onmouseover=x> | b |\n|---|---|\n| c | d |", "```\n<script>\n```", "<scr<script>ipt>", "`<script>`", "&lt;script&gt;"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		got := string(RenderMarkdown([]byte(src)))
		if bad := unsafeMarkup(got); bad != "" {
			t.Fatalf("input %q rendered unsafe markup %s in:\n%s", src, bad, got)
		}
	})
}

func TestUnsafeMarkupFindsWhatItLooksFor(t *testing.T) {
	for html, want := range map[string]bool{`<script>`: true, `<a onclick="x">`: true, `<a href="x onclick=y">`: false, `<p>on=1</p>`: false, `<td align="left">`: false} {
		if got := unsafeMarkup(html) != ""; got != want {
			t.Errorf("unsafeMarkup(%q) = %v, want %v", html, got, want)
		}
	}
}

func TestBindAddressTailnetOnly(t *testing.T) {
	lookup := func(addrs ...string) Lookup {
		return func(context.Context, string) ([]netip.Addr, error) {
			var out []netip.Addr
			for _, a := range addrs {
				out = append(out, netip.MustParseAddr(a))
			}
			return out, nil
		}
	}
	ctx := context.Background()
	for _, c := range []struct {
		address string
		lookup  Lookup
		want    string
		err     error
	}{
		{"host.tail.ts.net:4388", lookup("100.101.102.103"), "100.101.102.103:4388", nil},
		{"host.tail.ts.net:4388", lookup("fd7a:115c:a1e0::1"), "[fd7a:115c:a1e0::1]:4388", nil},
		{"host.tail.ts.net:4388", lookup("100.101.102.103", "fd7a:115c:a1e0::1"), "100.101.102.103:4388", nil},
		{"100.64.0.1:80", nil, "100.64.0.1:80", nil},
		{"host:4388", lookup("100.101.102.103", "192.168.1.5"), "", ErrNotTailnet},
		{"127.0.0.1:4388", nil, "", ErrNotTailnet},
		{"0.0.0.0:4388", nil, "", ErrNotTailnet},
		{"[::]:4388", nil, "", ErrNotTailnet},
		{"100.128.0.1:4388", nil, "", ErrNotTailnet},
		{"[::ffff:100.101.102.103]:4388", nil, "100.101.102.103:4388", nil},
	} {
		got, err := BindAddress(ctx, c.address, c.lookup)
		if got != c.want || (c.err != nil && !errors.Is(err, c.err)) || (c.err == nil && err != nil) {
			t.Errorf("BindAddress(%q) = %q, %v; want %q, %v", c.address, got, err, c.want, c.err)
		}
	}
	for _, address := range []string{"host", ":4388", "host:"} {
		if _, err := BindAddress(ctx, address, lookup("100.101.102.103")); err == nil {
			t.Errorf("BindAddress(%q) accepted", address)
		}
	}
	if _, err := BindAddress(ctx, "host:1", func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no such host") }); err == nil {
		t.Error("a host that does not resolve is accepted")
	}
	if _, err := BindAddress(ctx, "host:1", lookup()); err == nil {
		t.Error("a host with no addresses is accepted")
	}
	if _, err := Listen("127.0.0.1:0"); !errors.Is(err, ErrNotTailnet) {
		t.Errorf("Listen on loopback: %v, want ErrNotTailnet", err)
	}
}
