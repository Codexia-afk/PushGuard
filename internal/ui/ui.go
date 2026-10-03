package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/pushguard/pushguard/internal/approval"
	"github.com/pushguard/pushguard/internal/security"
	"io"
	"os"
	"strings"
	"time"
)

type UI struct {
	In             io.Reader
	Out            io.Writer
	Err            io.Writer
	Plain          bool
	NonInteractive bool
	reader         *bufio.Reader
	ctx            context.Context
}

func New(in io.Reader, out, err io.Writer, nonInteractive bool) *UI {
	return &UI{In: in, Out: out, Err: err, Plain: nonInteractive || !IsTTY(), NonInteractive: nonInteractive, reader: bufio.NewReader(in)}
}

// BindContext makes all decision prompts cancelable. Only one line is requested
// at a time: reading ahead could consume a later authorization prematurely.
func (u *UI) BindContext(ctx context.Context) { u.ctx = ctx }

func (u *UI) readLine() (string, error) {
	if u.ctx == nil || u.ctx.Done() == nil {
		return u.reader.ReadString('\n')
	}
	if err := u.ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := u.reader.ReadString('\n')
		done <- result{line, err}
	}()
	select {
	case <-u.ctx.Done():
		return "", u.ctx.Err()
	case r := <-done:
		if err := u.ctx.Err(); err != nil {
			return "", err
		}
		return r.line, r.err
	}
}
func (u *UI) Title(s string) {
	fmt.Fprintf(u.Out, "\n%s\n", security.Terminal(security.Redact(s)))
}
func (u *UI) Say(s string) { fmt.Fprintln(u.Out, security.Terminal(security.Redact(s))) }
func (u *UI) Status(symbol, name, detail string) {
	symbol = mapSymbol(symbol)
	name = security.Terminal(security.Redact(name))
	detail = security.Terminal(security.Redact(detail))
	if detail == "" {
		fmt.Fprintf(u.Out, "%s %s\n", symbol, name)
	} else {
		fmt.Fprintf(u.Out, "%s %-22s %s\n", symbol, name, detail)
	}
}
func (u *UI) Error(s string) { fmt.Fprintf(u.Err, "FAIL %s\n", security.Terminal(security.Redact(s))) }
func (u *UI) Prompt(question string, options ...string) bool {
	question = security.Terminal(security.Redact(question))
	if len(options) > 0 {
		fmt.Fprintf(u.Out, "%s\n%s\n> ", question, formatOptions(options))
	} else {
		fmt.Fprintf(u.Out, "%s\n> ", question)
	}
	if u.NonInteractive {
		fmt.Fprintln(u.Out, "no (non-interactive)")
		return false
	}
	line, err := u.readLine()
	if err != nil {
		return false
	}
	return approval.ParseDecision(line) == approval.DecisionAllow
}
func (u *UI) Choice(question string, options ...string) string {
	question = security.Terminal(security.Redact(question))
	fmt.Fprintf(u.Out, "%s\n%s\n> ", question, formatOptions(options))
	if u.NonInteractive {
		fmt.Fprintln(u.Out, "(non-interactive)")
		return ""
	}
	line, err := u.readLine()
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(line))
}

// Text preserves case for human-authored metadata. It never treats text as
// authorization and never launches an editor or evaluates a command.
func (u *UI) Text(question string) string {
	u.Say(question)
	if u.NonInteractive {
		return ""
	}
	line, err := u.readLine()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(line)
}
func Duration(d time.Duration) string { return fmt.Sprintf("%.2fs", d.Seconds()) }
func PrintJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func IsTTY() bool { fi, err := os.Stdout.Stat(); return err == nil && fi.Mode()&os.ModeCharDevice != 0 }

func mapSymbol(s string) string {
	switch s {
	case "✓":
		return "PASS"
	case "✗":
		return "FAIL"
	case "⚠":
		return "WARN"
	}
	return s
}

func formatOptions(options []string) string {
	var b strings.Builder
	width := 0
	for _, option := range options {
		if width > 0 {
			if width+2+len(option) > 88 {
				b.WriteByte('\n')
				width = 0
			} else {
				b.WriteString("  ")
				width += 2
			}
		}
		b.WriteString(option)
		width += len(option)
	}
	return b.String()
}
