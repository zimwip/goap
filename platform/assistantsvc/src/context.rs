//! The context of a turn (ADR 0092): what the person was looking at when they wrote the message, in three layers the
//! prompt reads most specific first. The web collects it, the service caps and sanitises it, it is used for that
//! turn only and never stored (the user message keeps a short description, Context::describe) (`context.go`).

use std::collections::BTreeMap;

use serde::Serialize;

use crate::service::{Error, MAX_CONTEXT_BYTES, MAX_SELECTION_BYTES};
use crate::util::{clip, floor_boundary, go_json};

// Caps of the context. Texts longer than their cap are cut, lists longer than theirs lose their tail; only the
// selection (which the web clips itself) and a context that still exceeds MaxContextBytes without its entities are
// refused.
pub const MAX_ENTITIES: usize = 40;
pub const MAX_ERRORS: usize = 10;
pub(crate) const MAX_LABEL_BYTES: usize = 200;
pub(crate) const MAX_ID_BYTES: usize = 100;
pub(crate) const MAX_KIND_BYTES: usize = 60;
pub(crate) const MAX_SUMMARY_BYTES: usize = 600;
pub(crate) const MAX_ACTION_BYTES: usize = 300;
pub(crate) const MAX_ERROR_BYTES: usize = 200;
pub(crate) const MAX_PROPS: usize = 6;
pub(crate) const MAX_PROP_BYTES: usize = 80;
pub(crate) const MAX_PARAMS: usize = 10;
pub(crate) const MAX_PARAM_BYTES: usize = 200;

/// Context is the snapshot of the page of a message.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Context {
    pub app: App,
    pub screen: Screen,
    pub focus: Focus,
}

/// App is the application: the active project and the tab.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct App {
    pub project: String,
    pub tab: Tab,
}

/// Tab is the active tab (kind and parameters, opaque but for a change: kind "change", parameter "id").
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Tab {
    pub kind: String,
    pub params: BTreeMap<String, String>,
}

/// Screen is what the open view shows.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Screen {
    pub kind: String,
    pub title: String,
    /// Summary is a short text of what the view shows.
    pub summary: String,
    pub entities: Vec<Entity>,
}

/// Entity is an element the screen shows, with a few small properties.
#[derive(Debug, Clone, Default, PartialEq, Serialize)]
pub struct Entity {
    #[serde(rename = "type", skip_serializing_if = "String::is_empty")]
    pub r#type: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub label: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub props: BTreeMap<String, String>,
}

/// Element is the element the person acts on.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Element {
    pub r#type: String,
    pub id: String,
    pub label: String,
}

/// Dialog is the dialog open on the screen.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Dialog {
    pub kind: String,
    pub title: String,
}

impl Dialog {
    fn is_empty(&self) -> bool {
        self.kind.is_empty() && self.title.is_empty()
    }
}

/// Focus is the most specific layer: the element, the selection, the dialog and the action in progress.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Focus {
    pub element: Option<Element>,
    pub selection: String,
    pub dialog: Dialog,
    pub pending_action: String,
    pub errors: Vec<String>,
    pub last_action: String,
}

/// clean removes control characters (a newline is kept when multiline, else becomes a space), collapses runs of
/// spaces in a single-line text and cuts the result to n bytes.
pub(crate) fn clean(s: &str, n: usize, multiline: bool) -> String {
    let mut b = String::with_capacity(s.len());
    let mut space = false;
    for r in s.chars() {
        if r == '\n' && multiline {
            b.push(r);
            space = false;
        } else if r.is_control() || r.is_whitespace() || r == '\u{2028}' || r == '\u{2029}' {
            if !space && !b.is_empty() {
                b.push(' ');
            }
            space = true;
        } else {
            b.push(r);
            space = false;
        }
    }
    let out = b.trim();
    out[..floor_boundary(out, n)].to_string()
}

fn clean_map(
    m: &BTreeMap<String, String>,
    max: usize,
    key_bytes: usize,
    val_bytes: usize,
) -> BTreeMap<String, String> {
    let mut out = BTreeMap::new();
    for (k, v) in m {
        if out.len() == max {
            break;
        }
        let ck = clean(k, key_bytes, false);
        if !ck.is_empty() {
            out.insert(ck, clean(v, val_bytes, false));
        }
    }
    out
}

impl Context {
    /// change is the change the page of the turn is about ("" for none): a change tab (its "id" parameter), else
    /// the focused element when it is a change.
    pub(crate) fn change(&self) -> String {
        if self.app.tab.kind == "change" {
            let id = self
                .app
                .tab
                .params
                .get("id")
                .map(|s| s.trim())
                .unwrap_or("");
            if !id.is_empty() {
                return id.to_string();
            }
        }
        if let Some(e) = &self.focus.element
            && e.r#type == "change"
        {
            return e.id.clone();
        }
        String::new()
    }

    /// normalise returns the context capped and sanitised. Entities beyond MaxEntities, then the last ones until the
    /// rendered context fits MaxContextBytes, are dropped, the entity of the focus first kept.
    pub(crate) fn normalise(&self) -> Result<Context, Error> {
        let c = self;
        if c.focus.selection.len() > MAX_SELECTION_BYTES {
            return Err(Error::Invalid(format!(
                "the selection is {} bytes, at most {MAX_SELECTION_BYTES}",
                c.focus.selection.len()
            )));
        }
        let mut o = Context {
            app: App {
                project: clean(&c.app.project, MAX_ID_BYTES, false),
                tab: Tab {
                    kind: clean(&c.app.tab.kind, MAX_KIND_BYTES, false),
                    params: clean_map(&c.app.tab.params, MAX_PARAMS, 40, MAX_PARAM_BYTES),
                },
            },
            screen: Screen {
                kind: clean(&c.screen.kind, MAX_KIND_BYTES, false),
                title: clean(&c.screen.title, MAX_LABEL_BYTES, false),
                summary: clean(&c.screen.summary, MAX_SUMMARY_BYTES, true),
                entities: Vec::new(),
            },
            focus: Focus {
                element: None,
                selection: clean(&c.focus.selection, MAX_SELECTION_BYTES, true),
                dialog: Dialog {
                    kind: clean(&c.focus.dialog.kind, MAX_KIND_BYTES, false),
                    title: clean(&c.focus.dialog.title, MAX_LABEL_BYTES, false),
                },
                pending_action: clean(&c.focus.pending_action, MAX_ACTION_BYTES, false),
                errors: Vec::new(),
                last_action: clean(&c.focus.last_action, MAX_ACTION_BYTES, false),
            },
        };
        if let Some(e) = &c.focus.element {
            let el = Element {
                r#type: clean(&e.r#type, MAX_KIND_BYTES, false),
                id: clean(&e.id, MAX_ID_BYTES, false),
                label: clean(&e.label, MAX_LABEL_BYTES, false),
            };
            if el != Element::default() {
                o.focus.element = Some(el);
            }
        }
        for e in &c.focus.errors {
            let e = clean(e, MAX_ERROR_BYTES, false);
            if !e.is_empty() && o.focus.errors.len() < MAX_ERRORS {
                o.focus.errors.push(e);
            }
        }
        for e in &c.screen.entities {
            let ce = Entity {
                r#type: clean(&e.r#type, MAX_KIND_BYTES, false),
                id: clean(&e.id, MAX_ID_BYTES, false),
                label: clean(&e.label, MAX_LABEL_BYTES, false),
                state: clean(&e.state, MAX_KIND_BYTES, false),
                props: clean_map(&e.props, MAX_PROPS, 40, MAX_PROP_BYTES),
            };
            if ce.r#type.is_empty() && ce.id.is_empty() && ce.label.is_empty() {
                continue;
            }
            o.screen.entities.push(ce);
        }
        // the entity the person acts on comes first, so that it is the last one dropped
        if let Some(el) = &o.focus.element
            && let Some(i) = o
                .screen
                .entities
                .iter()
                .position(|e| e.r#type == el.r#type && e.id == el.id)
            && i > 0
        {
            let e = o.screen.entities.remove(i);
            o.screen.entities.insert(0, e);
        }
        o.screen.entities.truncate(MAX_ENTITIES);
        while o.rendered("") > MAX_CONTEXT_BYTES && !o.screen.entities.is_empty() {
            o.screen.entities.pop();
        }
        let n = o.rendered("");
        if n > MAX_CONTEXT_BYTES {
            return Err(Error::Invalid(format!(
                "the context is {n} bytes, at most {MAX_CONTEXT_BYTES}"
            )));
        }
        Ok(o)
    }

    /// rendered is the size of the JSON of the context, `project` replacing the active project when not empty
    /// (select_project moves it during the turn).
    pub(crate) fn rendered(&self, project: &str) -> usize {
        self.render(project).len()
    }

    /// render is the JSON of the prompt: the layers most specific first, empty fields left out.
    pub(crate) fn render(&self, project: &str) -> String {
        let f = &self.focus;
        let focus = if f.element.is_some()
            || !f.selection.is_empty()
            || !f.dialog.is_empty()
            || !f.pending_action.is_empty()
            || !f.errors.is_empty()
            || !f.last_action.is_empty()
        {
            Some(JsonFocus {
                element: f.element.as_ref().map(|e| JsonElement {
                    r#type: &e.r#type,
                    id: &e.id,
                    label: &e.label,
                }),
                selection: &f.selection,
                dialog: (!f.dialog.is_empty()).then_some(JsonDialog {
                    kind: &f.dialog.kind,
                    title: &f.dialog.title,
                }),
                pending_action: &f.pending_action,
                errors: &f.errors,
                last_action: &f.last_action,
            })
        } else {
            None
        };
        let s = &self.screen;
        let screen = (!s.kind.is_empty()
            || !s.title.is_empty()
            || !s.summary.is_empty()
            || !s.entities.is_empty())
        .then_some(JsonScreen {
            kind: &s.kind,
            title: &s.title,
            summary: &s.summary,
            entities: &s.entities,
        });
        let project = if project.is_empty() {
            self.app.project.as_str()
        } else {
            project
        };
        let tab = &self.app.tab;
        let app =
            (!project.is_empty() || !tab.kind.is_empty() || !tab.params.is_empty()).then(|| {
                JsonApp {
                    project,
                    tab: (!tab.kind.is_empty() || !tab.params.is_empty()).then_some(JsonTab {
                        kind: &tab.kind,
                        params: &tab.params,
                    }),
                }
            });
        go_json(&JsonContext { focus, screen, app })
    }

    /// describe is the short description of the context kept on the user message: what the person was doing, on
    /// what, in which screen, tab and project, and that a selection existed. Never the entities, the summary, the
    /// errors, the selection or any form content.
    pub(crate) fn describe(&self) -> String {
        let mut parts: Vec<String> = Vec::new();
        if !self.focus.pending_action.is_empty() {
            parts.push(clip(&self.focus.pending_action, 80));
        }
        if let Some(e) = &self.focus.element {
            let name = if e.id.is_empty() { &e.label } else { &e.id };
            parts.push(
                format!("on {} {}", clip(&e.r#type, 40), clip(name, 80))
                    .trim()
                    .to_string(),
            );
        }
        let d = &self.focus.dialog;
        if !d.is_empty() {
            parts.push(format!(
                "dialog {}",
                clip(format!("{} {}", d.kind, d.title).trim(), 80)
            ));
        }
        let s = &self.screen;
        if !s.kind.is_empty() || !s.title.is_empty() {
            parts.push(format!(
                "screen {}",
                clip(format!("{} {}", s.kind, s.title).trim(), 100)
            ));
        }
        if !self.app.tab.kind.is_empty() {
            parts.push(format!("tab {}", clip(&self.app.tab.kind, 40)));
        }
        if !self.app.project.is_empty() {
            parts.push(format!("project {}", clip(&self.app.project, 60)));
        }
        if !self.focus.selection.is_empty() {
            parts.push(format!(
                "{} selected characters",
                self.focus.selection.chars().count()
            ));
        }
        clip(&parts.join(", "), goap_convsvc::MAX_CONTEXT_BYTES)
    }
}

// The JSON of the prompt: the layers most specific first, empty fields left out.
#[derive(Serialize)]
struct JsonContext<'a> {
    #[serde(skip_serializing_if = "Option::is_none")]
    focus: Option<JsonFocus<'a>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    screen: Option<JsonScreen<'a>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    app: Option<JsonApp<'a>>,
}

#[derive(Serialize)]
struct JsonFocus<'a> {
    #[serde(skip_serializing_if = "Option::is_none")]
    element: Option<JsonElement<'a>>,
    #[serde(skip_serializing_if = "str::is_empty")]
    selection: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    dialog: Option<JsonDialog<'a>>,
    #[serde(rename = "pendingAction", skip_serializing_if = "str::is_empty")]
    pending_action: &'a str,
    #[serde(skip_serializing_if = "<[String]>::is_empty")]
    errors: &'a [String],
    #[serde(rename = "lastAction", skip_serializing_if = "str::is_empty")]
    last_action: &'a str,
}

#[derive(Serialize)]
struct JsonElement<'a> {
    #[serde(rename = "type", skip_serializing_if = "str::is_empty")]
    r#type: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    id: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    label: &'a str,
}

#[derive(Serialize)]
struct JsonDialog<'a> {
    #[serde(skip_serializing_if = "str::is_empty")]
    kind: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    title: &'a str,
}

#[derive(Serialize)]
struct JsonScreen<'a> {
    #[serde(skip_serializing_if = "str::is_empty")]
    kind: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    title: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    summary: &'a str,
    #[serde(skip_serializing_if = "<[Entity]>::is_empty")]
    entities: &'a [Entity],
}

#[derive(Serialize)]
struct JsonApp<'a> {
    #[serde(rename = "activeProject", skip_serializing_if = "str::is_empty")]
    project: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    tab: Option<JsonTab<'a>>,
}

#[derive(Serialize)]
struct JsonTab<'a> {
    #[serde(skip_serializing_if = "str::is_empty")]
    kind: &'a str,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    params: &'a BTreeMap<String, String>,
}
