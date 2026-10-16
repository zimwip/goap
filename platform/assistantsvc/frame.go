package assistantsvc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/platform/convsvc"
)

// The frame of a turn: where the person is (the active project, the change they are looking at and its methodology),
// computed by the server from the context of the turn and the history of the conversation, never from the words of the
// model. The tools the model gets and the prompt follow it: on a change, the tools of a change; elsewhere, those of a
// project. Creating a change (create_change, start_agent with a newChange) waits until the open changes were searched
// in this conversation (search_changes), so the assistant proposes an existing change before a new one.

// Level is how specific the frame is.
type Level string

const (
	// LevelProject: no change in the context; the active project (the root project when none is named).
	LevelProject Level = "project"
	// LevelChange: the person is looking at a change they can read.
	LevelChange Level = "change"
)

// Frame is where the turn stands.
type Frame struct {
	Level Level
	// Project is the active project ("" when none is named: the root project).
	Project string
	// Change and Methodology are the change of the context and its methodology, at level change.
	Change, Methodology string
	// Note says why a change of the context is not the frame (it cannot be read).
	Note string
}

// levelTools are the server tools of each level, in the order they are declared to the model.
var levelTools = map[Level][]string{
	LevelProject: {ToolSelectProject, ToolOpenChange, ToolListMethodologies, ToolMethodologyQuery, ToolSearchChanges, ToolListAgents, ToolStartAgent, ToolCreateChange},
	LevelChange:  {ToolSelectProject, ToolOpenChange, ToolMethodologyQuery, ToolListAgents, ToolStartAgent, ToolSearchChanges, ToolAttachChange, ToolDetachChange},
}

// tools are the server tools of the frame; create_change only once the open changes were searched.
func (f Frame) tools(searched bool) []string {
	var out []string
	for _, n := range levelTools[f.Level] {
		if n == ToolCreateChange && !searched {
			continue
		}
		out = append(out, n)
	}
	return out
}

// mayCreate reports whether a new change may be proposed in this frame: at level project, once searched.
func (f Frame) mayCreate(searched bool) bool { return f.Level == LevelProject && searched }

// render is the frame section of the prompt.
func (f Frame) render(tools []string, searched bool) string {
	project := f.Project
	if project == "" {
		project = "(none named: the root project)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Frame of this turn (computed by the platform):\n- level: %s\n- project: %s\n", f.Level, project)
	if f.Level == LevelChange {
		m := f.Methodology
		if m == "" {
			m = "(none)"
		}
		fmt.Fprintf(&b, "- change: %s\n- methodology: %s\n", f.Change, m)
	}
	if f.Note != "" {
		fmt.Fprintf(&b, "- note: %s\n", f.Note)
	}
	fmt.Fprintf(&b, "- server tools of this turn: %s\n", strings.Join(tools, ", "))
	switch {
	case f.Level == LevelChange:
		b.WriteString("- creating a change is not offered on a change: work on this one, or search the open changes (search_changes) and open another\n")
	case !searched:
		b.WriteString("- create_change (and start_agent with a newChange) are offered once the open changes were searched (search_changes) in this conversation\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// frame computes the frame of the turn now: the project may have changed (select_project); the change of the context
// is read once, as the caller.
func (t *turn) frame(ctx context.Context) Frame {
	f := Frame{Level: LevelProject, Project: t.project}
	id := t.in.Context.change()
	if id == "" {
		return f
	}
	if !t.changeRead {
		t.changeRead = true
		ch, err := t.s.readChange(ctx, t.p, id)
		if err != nil {
			t.changeNote = err.Error()
		} else {
			t.changeMethodology = ch.Methodology
			t.changeOK = true
		}
	}
	if !t.changeOK {
		f.Note = t.changeNote
		return f
	}
	f.Level, f.Change, f.Methodology = LevelChange, id, t.changeMethodology
	return f
}

// available checks that a server tool is one of the frame's; the error names the tools of the turn.
func (t *turn) available(ctx context.Context, name string) error {
	f := t.frame(ctx)
	tools := f.tools(t.searched)
	if slices.Contains(tools, name) {
		return nil
	}
	why := fmt.Sprintf("tool %q is not available at level %s", name, f.Level)
	if name == ToolCreateChange && f.Level == LevelProject {
		why = fmt.Sprintf("tool %q is not available yet: search the open changes first (search_changes) and show the person what was found", name)
	}
	return fmt.Errorf("%s; the tools of this turn are %s (and the screen tools prefixed %q)", why, strings.Join(tools, ", "), PrefixUI)
}

// searchedIn reports whether the open changes were searched in an earlier turn of the conversation: an assistant
// message carries a search_changes action.
func searchedIn(msgs []convsvc.Message) bool {
	return slices.ContainsFunc(msgs, func(m convsvc.Message) bool {
		return m.Role == convsvc.RoleAssistant && slices.ContainsFunc(m.Actions, func(a convsvc.Action) bool { return a["type"] == ActionSearchChanges })
	})
}
