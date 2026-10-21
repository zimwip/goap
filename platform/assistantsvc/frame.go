package assistantsvc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/platform/convsvc"
)

// The frame of a turn: where the person works (the active project, the working change they selected and its methodology),
// computed by the server from the context of the turn, the history of the conversation and the tools run in the turn,
// never from the words of the model. The tools the model gets and the prompt follow it: outside any project (none
// named), the tools that find one or a change; in a project, those of a project; on a change, those of a change. A
// tool that sets the context moves the frame for the next rounds of the turn (select_project to the project,
// open_change to the change), so the assistant reaches the rest of the tools from there. A change the person only looks
// at (a change tab that is not the working change) is not the frame: the prompt names it, and open_change works in it. Creating a change (create_change, start_agent with a newChange) waits until the open changes were searched
// in this conversation (search_changes), so the assistant proposes an existing change before a new one.

// Level is how specific the frame is.
type Level string

const (
	// LevelNone: no project is named and no change is in the context: the assistant works outside any project.
	LevelNone Level = "none"
	// LevelProject: no change in the context; the active project.
	LevelProject Level = "project"
	// LevelChange: the turn works on a change the person can read: the working change, or the one open_change opened.
	LevelChange Level = "change"
)

// Frame is where the turn stands.
type Frame struct {
	Level Level
	// Project is the active project ("" at level none), at level change the project of the change.
	Project string
	// Change and Methodology are the change of the context and its methodology, at level change.
	Change, Methodology string
	// Note says why a change of the context is not the frame (it cannot be read, or belongs to another project than
	// the one select_project moved to).
	Note string
	// Viewed is a change the page shows that is not the change of the frame ("" for none).
	Viewed string
}

// levelTools are the server tools of each level, in the order they are declared to the model.
var levelTools = map[Level][]string{
	LevelNone:    {ToolListProjects, ToolSelectProject, ToolSearchChanges, ToolOpenChange},
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
		project = "(none: outside any project)"
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
	if f.Viewed != "" {
		fmt.Fprintf(&b, "- the person is looking at change %s, which is not the working change: to work on it, open it (open_change)\n", f.Viewed)
	}
	fmt.Fprintf(&b, "- server tools of this turn: %s\n", strings.Join(tools, ", "))
	switch {
	case f.Level == LevelNone:
		b.WriteString("- outside any project only these tools are offered: find a project (list_projects) and select it (select_project), or find a change (search_changes) and open it (open_change); the tools of that project or change are then offered in the next step of this turn\n")
	case f.Level == LevelChange:
		b.WriteString("- creating a change is not offered on a change: work on this one, or search the open changes (search_changes) and open another\n")
	case !searched:
		b.WriteString("- create_change (and start_agent with a newChange) are offered once the open changes were searched (search_changes) in this conversation\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// frame computes the frame of the turn now: the project may have changed (select_project), a change may have been
// opened (open_change, else the change of the context); the change is read once, as the caller.
func (t *turn) frame(ctx context.Context) Frame {
	f := Frame{Level: LevelProject, Project: t.project}
	if f.Project == "" {
		f.Level = LevelNone
	}
	id := t.change()
	if v := t.in.Context.viewed(); v != "" && v != id {
		f.Viewed = v
	}
	if id == "" {
		return f
	}
	if t.changeRead != id {
		t.changeRead, t.changeOK, t.changeNote = id, false, ""
		ch, err := t.s.readChange(ctx, t.p, id)
		if err != nil {
			t.changeNote = err.Error()
		} else {
			t.changeMethodology, t.changeProject = ch.Methodology, ch.ProjectID.Key
			t.changeOK = true
		}
	}
	if !t.changeOK {
		f.Note = t.changeNote
		return f
	}
	// select_project moved the turn to another project: the working change of the project left is not worked on
	if t.opened == "" && t.project != t.in.Context.App.Project && t.changeProject != "" && t.changeProject != t.project {
		f.Note = fmt.Sprintf("the working change %s belongs to project %s, not to the project selected in this turn", id, t.changeProject)
		return f
	}
	f.Level, f.Change, f.Methodology = LevelChange, id, t.changeMethodology
	if t.changeProject != "" {
		f.Project = t.changeProject
	}
	return f
}

// workChange is the change the tools of the turn work on: change(), unless select_project moved the turn to another
// project than the working change's (frame). A change that cannot be read is still named, so the tool says why.
func (t *turn) workChange(ctx context.Context) string {
	id := t.change()
	if id != "" && t.frame(ctx).Change == "" && t.changeOK {
		return ""
	}
	return id
}

// change is the change of the turn: the one open_change opened, else the working change of the context.
func (t *turn) change() string {
	if t.opened != "" {
		return t.opened
	}
	return t.in.Context.change()
}

// describe is the frame in a few words, recorded on the answer: where the assistant worked.
func (f Frame) describe() string {
	var parts []string
	switch f.Project {
	case "":
		parts = append(parts, "outside any project")
	default:
		parts = append(parts, "project "+f.Project)
	}
	if f.Level == LevelChange {
		c := "change " + f.Change
		if f.Methodology != "" {
			c += " (" + f.Methodology + ")"
		}
		parts = append(parts, c)
	}
	return clip(strings.Join(parts, " · "), convsvc.MaxContextBytes)
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
