package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
)

var now = time.Date(2026, 10, 27, 15, 30, 0, 0, time.UTC)

// fakeBackend records every call the screens make and answers from its
// fields.
type fakeBackend struct {
	mu       sync.Mutex
	calls    []string
	requests []ScanRequest
	selected [][]finding.Finding
	roots    [][]string

	report  *finding.Report
	events  []scan.Event
	scanErr error
	// wait makes Scan wait for its context to end and return what it has,
	// as a scan stopped with esc does.
	wait  bool
	saved SavedPlan
}

func (f *fakeBackend) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeBackend) Scan(ctx context.Context, req ScanRequest, progress func(scan.Event)) (*finding.Report, error) {
	f.record("Scan")
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	for _, ev := range f.events {
		progress(ev)
	}
	if f.wait {
		<-ctx.Done()
		r := *f.report
		r.Warnings = append(r.Warnings, finding.Warning{Message: "scan interrupted: " + ctx.Err().Error()})
		return &r, nil
	}
	return f.report, f.scanErr
}

func (f *fakeBackend) Protected(roots []string) []apply.ProtectedEntry {
	f.record("Protected")
	f.mu.Lock()
	f.roots = append(f.roots, roots)
	f.mu.Unlock()
	return apply.ProtectedList(apply.ProtectedOptions{Home: "/Users/me", Roots: roots, Exclude: []string{"/Users/me/Code/archive"}})
}

func (f *fakeBackend) SavePlan(_ context.Context, r *finding.Report, selected []finding.Finding) (SavedPlan, error) {
	f.record("SavePlan")
	f.mu.Lock()
	f.selected = append(f.selected, selected)
	f.mu.Unlock()
	if f.saved.Path == "" {
		p, err := plan.New(r, selected, "laptop", now)
		if err != nil {
			return SavedPlan{}, err
		}
		return SavedPlan{Path: "/Users/me/Library/Caches/reclaim/plans/plan-20261027T153000Z.json", Plan: p}, nil
	}
	return f.saved, nil
}

// harness drives a model the way bubbletea does: keys go to Update, the
// commands it returns run, and the messages they and the backend send come
// back to Update.
type harness struct {
	t  *testing.T
	m  *Model
	be *fakeBackend

	mu   sync.Mutex
	sent []tea.Msg

	// background makes the next command run in the background, and
	// pending holds the messages of such commands.
	background bool
	pending    []chan tea.Msg
	quit       bool
}

func categories() []CategoryChoice {
	out := make([]CategoryChoice, 0, len(finding.Categories()))
	for _, c := range finding.Categories() {
		out = append(out, CategoryChoice{Category: c, On: true})
	}
	return out
}

func newHarness(t *testing.T, be *fakeBackend, start StartOptions, width, height int) *harness {
	t.Helper()
	h := &harness{t: t, be: be}
	if start.Categories == nil {
		start.Categories = categories()
	}
	if start.Roots == nil {
		start.Roots = []string{"/Users/me/Code", "/Users/me/Downloads"}
	}
	h.m = New(Options{
		Backend:  be,
		Send:     h.send,
		Home:     "/Users/me",
		Version:  "v0.3.0",
		Now:      func() time.Time { return now },
		Renderer: lipgloss.NewRenderer(io.Discard),
		Start:    start,
		OutPath:  "plan.json",
	})
	h.m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return h
}

func (h *harness) send(msg tea.Msg) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, msg)
}

// press feeds keys and runs the commands they return. Named keys are
// "space", "enter", "esc", "up", "down", "left", "right" and "ctrl+c";
// anything else is typed as runes.
func (h *harness) press(keys ...string) {
	h.t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		case "home":
			msg = tea.KeyMsg{Type: tea.KeyHome}
		case "ctrl+u":
			msg = tea.KeyMsg{Type: tea.KeyCtrlU}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		h.update(msg)
	}
}

// update sends a message and runs what follows from it.
func (h *harness) update(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.run(cmd)
}

// run runs a command and delivers its message. A command the test said
// will wait, such as a scan that waits for esc, runs in the background
// until settle collects it.
func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()
	if cmd == nil {
		return
	}
	if h.background {
		h.background = false
		done := make(chan tea.Msg, 1)
		go func() { done <- cmd() }()
		h.pending = append(h.pending, done)
		return
	}
	h.deliver(cmd())
}

// deliver gives the model the messages of its own commands. Others, such as
// the cursor blinks of a text input, are dropped, so they do not loop.
func (h *harness) deliver(msg tea.Msg) {
	h.t.Helper()
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	case tea.QuitMsg:
		h.quit = true
	case scanDoneMsg, planSavedMsg:
		h.flushSent()
		h.update(msg)
	}
}

// flushSent delivers what the backend sent through Options.Send.
func (h *harness) flushSent() {
	h.t.Helper()
	h.mu.Lock()
	sent := h.sent
	h.sent = nil
	h.mu.Unlock()
	for _, msg := range sent {
		h.update(msg)
	}
}

// settle waits for the commands that were waiting and delivers their
// messages.
func (h *harness) settle() {
	h.t.Helper()
	pending := h.pending
	h.pending = nil
	for _, done := range pending {
		select {
		case msg := <-done:
			h.deliver(msg)
		case <-time.After(5 * time.Second):
			h.t.Fatal("a command never finished")
		}
	}
}

func (h *harness) view() string { return h.m.View() }

// flat joins the words of the view, so wrapped lines compare as one.
func (h *harness) flat() string { return strings.Join(strings.Fields(h.view()), " ") }

func (h *harness) calls() []string {
	h.be.mu.Lock()
	defer h.be.mu.Unlock()
	return append([]string(nil), h.be.calls...)
}

// moveTo moves the cursor of the Review screen to the first row containing
// s.
func (h *harness) moveTo(s string) {
	h.t.Helper()
	h.press("home")
	for range 500 {
		if strings.Contains(h.cursorRow(), s) {
			return
		}
		h.press("down")
	}
	h.t.Fatalf("no row %q in\n%s", s, h.view())
}

// cursorRow is the line of the view that holds the cursor.
func (h *harness) cursorRow() string {
	for l := range strings.SplitSeq(h.view(), "\n") {
		if strings.HasPrefix(l, "> ") {
			return l
		}
	}
	return ""
}

var errScan = errors.New("no such folder: /Users/me/nope")

// details is the text of the details pane, its words joined, wide enough
// that nothing is cut.
func (h *harness) details() string {
	return strings.Join(strings.Fields(strings.Join(h.m.review.pane(400, 400), " ")), " ")
}
