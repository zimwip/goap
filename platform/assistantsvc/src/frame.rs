//! The frame of a turn (`frame.go`): where the person is (the active project, the change they are looking at and its
//! methodology), computed by the server from the context of the turn and the history of the conversation, never from
//! the words of the model. The tools the model gets and the prompt follow it: on a change, the tools of a change;
//! elsewhere, those of a project. Creating a change (create_change, start_agent with a newChange) waits until the open
//! changes were searched in this conversation (search_changes), so the assistant proposes an existing change before a
//! new one.

use std::fmt::Write as _;

use goap_convsvc as convsvc;
use goap_ctx::Ctx;

use crate::agents::{TOOL_LIST_AGENTS, TOOL_START_AGENT};
use crate::methodtool::TOOL_METHODOLOGY_QUERY;
use crate::relate::{TOOL_ATTACH_CHANGE, TOOL_DETACH_CHANGE};
use crate::searchtool::{ACTION_SEARCH_CHANGES, TOOL_SEARCH_CHANGES};
use crate::turn::{
    TOOL_CREATE_CHANGE, TOOL_LIST_METHODOLOGIES, TOOL_OPEN_CHANGE, TOOL_SELECT_PROJECT, Turn,
};
use crate::uitools::PREFIX_UI;
use crate::util::{q, sv};

/// Level is how specific the frame is.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Level {
    /// No change in the context; the active project (the root project when none is named).
    #[default]
    Project,
    /// The person is looking at a change they can read.
    Change,
}

impl Level {
    /// The name of the level ("project", "change").
    pub fn as_str(&self) -> &'static str {
        match self {
            Level::Project => "project",
            Level::Change => "change",
        }
    }
}

impl std::fmt::Display for Level {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}

/// Frame is where the turn stands.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Frame {
    pub level: Level,
    /// The active project ("" when none is named: the root project).
    pub project: String,
    /// The change of the context, at level change.
    pub change: String,
    /// Its methodology, at level change.
    pub methodology: String,
    /// Why a change of the context is not the frame (it cannot be read).
    pub note: String,
}

/// levelTools are the server tools of each level, in the order they are declared to the model.
pub(crate) fn level_tools(l: Level) -> &'static [&'static str] {
    match l {
        Level::Project => &[
            TOOL_SELECT_PROJECT,
            TOOL_OPEN_CHANGE,
            TOOL_LIST_METHODOLOGIES,
            TOOL_METHODOLOGY_QUERY,
            TOOL_SEARCH_CHANGES,
            TOOL_LIST_AGENTS,
            TOOL_START_AGENT,
            TOOL_CREATE_CHANGE,
        ],
        Level::Change => &[
            TOOL_SELECT_PROJECT,
            TOOL_OPEN_CHANGE,
            TOOL_METHODOLOGY_QUERY,
            TOOL_LIST_AGENTS,
            TOOL_START_AGENT,
            TOOL_SEARCH_CHANGES,
            TOOL_ATTACH_CHANGE,
            TOOL_DETACH_CHANGE,
        ],
    }
}

impl Frame {
    /// The server tools of the frame; create_change only once the open changes were searched.
    pub fn tools(&self, searched: bool) -> Vec<String> {
        level_tools(self.level)
            .iter()
            .filter(|n| !(**n == TOOL_CREATE_CHANGE && !searched))
            .map(|n| n.to_string())
            .collect()
    }

    /// Whether a new change may be proposed in this frame: at level project, once searched.
    pub(crate) fn may_create(&self, searched: bool) -> bool {
        self.level == Level::Project && searched
    }

    /// The frame section of the prompt.
    pub(crate) fn render(&self, tools: &[String], searched: bool) -> String {
        let project = if self.project.is_empty() {
            "(none named: the root project)"
        } else {
            &self.project
        };
        let mut b = String::new();
        let _ = write!(
            b,
            "Frame of this turn (computed by the platform):\n- level: {}\n- project: {}\n",
            self.level, project
        );
        if self.level == Level::Change {
            let m = if self.methodology.is_empty() {
                "(none)"
            } else {
                &self.methodology
            };
            let _ = write!(b, "- change: {}\n- methodology: {}\n", self.change, m);
        }
        if !self.note.is_empty() {
            let _ = writeln!(b, "- note: {}", self.note);
        }
        let _ = writeln!(b, "- server tools of this turn: {}", tools.join(", "));
        if self.level == Level::Change {
            b.push_str("- creating a change is not offered on a change: work on this one, or search the open changes (search_changes) and open another\n");
        } else if !searched {
            b.push_str("- create_change (and start_agent with a newChange) are offered once the open changes were searched (search_changes) in this conversation\n");
        }
        b.trim_end_matches('\n').to_string()
    }
}

impl Turn {
    /// frame computes the frame of the turn now: the project may have changed (select_project); the change of the
    /// context is read once, as the caller.
    pub(crate) async fn frame(&mut self, ctx: &Ctx) -> Frame {
        let mut f = Frame {
            level: Level::Project,
            project: self.project.clone(),
            ..Default::default()
        };
        let id = self.in_.context.change();
        if id.is_empty() {
            return f;
        }
        if !self.change_read {
            self.change_read = true;
            match self.s.read_change(ctx, &self.p, &id).await {
                Err(e) => self.change_note = e,
                Ok(ch) => {
                    self.change_methodology = ch.methodology.clone();
                    self.change_ok = true;
                }
            }
        }
        if !self.change_ok {
            f.note = self.change_note.clone();
            return f;
        }
        f.level = Level::Change;
        f.change = id;
        f.methodology = self.change_methodology.clone();
        f
    }

    /// available checks that a server tool is one of the frame's; the error names the tools of the turn.
    pub(crate) async fn available(&mut self, ctx: &Ctx, name: &str) -> Result<(), String> {
        let f = self.frame(ctx).await;
        let tools = f.tools(self.searched);
        if tools.iter().any(|t| t == name) {
            return Ok(());
        }
        let why = if name == TOOL_CREATE_CHANGE && f.level == Level::Project {
            format!(
                "tool {} is not available yet: search the open changes first (search_changes) and show the person what was found",
                q(name)
            )
        } else {
            format!("tool {} is not available at level {}", q(name), f.level)
        };
        Err(format!(
            "{why}; the tools of this turn are {} (and the screen tools prefixed {})",
            tools.join(", "),
            q(PREFIX_UI)
        ))
    }
}

/// searchedIn reports whether the open changes were searched in an earlier turn of the conversation: an assistant
/// message carries a search_changes action.
pub(crate) fn searched_in(msgs: &[convsvc::Message]) -> bool {
    msgs.iter().any(|m| {
        m.role == convsvc::ROLE_ASSISTANT
            && m.actions
                .iter()
                .any(|a| sv(a, "type") == ACTION_SEARCH_CHANGES)
    })
}
