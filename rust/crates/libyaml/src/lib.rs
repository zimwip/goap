//! The events of libyaml's parser (the C library `gopkg.in/yaml.v3` ports to Go), read through
//! `unsafe-libyaml-norway` behind a safe [`Parser`]: a document scans, parses and fails here as it does in Go, with the
//! same problems at the same marks.
#![allow(unsafe_code)]

use std::{ffi::CStr, mem::MaybeUninit, ptr::NonNull};

use unsafe_libyaml_norway as unsafe_sys;

/// A position in the input, from 0.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Mark {
    pub index: u64,
    pub line: u64,
    pub column: u64,
}

impl From<unsafe_sys::yaml_mark_t> for Mark {
    fn from(m: unsafe_sys::yaml_mark_t) -> Self {
        Mark {
            index: m.index,
            line: m.line,
            column: m.column,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ScalarStyle {
    Plain,
    SingleQuoted,
    DoubleQuoted,
    Literal,
    Folded,
}

/// What an event holds.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum EventKind {
    StreamStart,
    StreamEnd,
    DocumentStart,
    DocumentEnd,
    Alias {
        anchor: String,
    },
    Scalar {
        anchor: Option<String>,
        tag: Option<String>,
        value: String,
        style: ScalarStyle,
    },
    SequenceStart {
        anchor: Option<String>,
        tag: Option<String>,
        flow: bool,
    },
    SequenceEnd,
    MappingStart {
        anchor: Option<String>,
        tag: Option<String>,
        flow: bool,
    },
    MappingEnd,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Event {
    pub kind: EventKind,
    pub start: Mark,
    pub end: Mark,
}

/// The stage of the parser a problem was found at.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ErrorKind {
    Memory,
    Reader,
    Scanner,
    Parser,
    Other,
}

/// A problem of the input, as the parser records it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error {
    pub kind: ErrorKind,
    pub problem: String,
    pub problem_offset: u64,
    pub problem_mark: Mark,
    pub context: Option<String>,
    pub context_mark: Mark,
}

struct Pinned {
    sys: unsafe_sys::yaml_parser_t,
    /// the bytes the parser reads, kept as long as it lives (it holds a pointer to them)
    _input: Box<[u8]>,
}

/// A parser over a document's bytes, read event by event.
pub struct Parser {
    pin: NonNull<Pinned>,
    done: bool,
}

// SAFETY: the parser owns its state and its input; nothing of it is shared with another thread.
unsafe impl Send for Parser {}

impl Parser {
    pub fn new(input: &[u8]) -> Parser {
        let input: Box<[u8]> = input.into();
        let mut boxed = Box::new(MaybeUninit::<Pinned>::uninit());
        let ptr = boxed.as_mut_ptr();
        // SAFETY: the parser is initialized in place before anything reads it; the input lives in the same
        // allocation's owner and does not move (a boxed slice), so the pointer the parser keeps stays valid.
        unsafe {
            let parser = std::ptr::addr_of_mut!((*ptr).sys);
            if unsafe_sys::yaml_parser_initialize(parser).fail {
                panic!("libyaml: cannot allocate a parser");
            }
            unsafe_sys::yaml_parser_set_encoding(parser, unsafe_sys::YAML_UTF8_ENCODING);
            unsafe_sys::yaml_parser_set_input_string(parser, input.as_ptr(), input.len() as u64);
            std::ptr::addr_of_mut!((*ptr)._input).write(input);
        }
        let pin = NonNull::new(Box::into_raw(boxed).cast::<Pinned>()).expect("a boxed pointer");
        Parser { pin, done: false }
    }

    /// The next event, or the problem that stops the parse (the same one again on every later call).
    pub fn next_event(&mut self) -> Result<Event, Error> {
        // SAFETY: the parser is initialized (`new`) and only this value reaches it; an event is read whole, copied,
        // then deleted.
        unsafe {
            let parser = std::ptr::addr_of_mut!((*self.pin.as_ptr()).sys);
            if failed(parser) {
                return Err(error_of(parser));
            }
            if self.done {
                return Ok(Event {
                    kind: EventKind::StreamEnd,
                    start: Mark::default(),
                    end: Mark::default(),
                });
            }
            let mut event = MaybeUninit::<unsafe_sys::yaml_event_t>::uninit();
            let ev = event.as_mut_ptr();
            if unsafe_sys::yaml_parser_parse(parser, ev).fail || failed(parser) {
                return Err(error_of(parser));
            }
            let out = Event {
                kind: kind_of(&*ev),
                start: (*ev).start_mark.into(),
                end: (*ev).end_mark.into(),
            };
            unsafe_sys::yaml_event_delete(ev);
            self.done = out.kind == EventKind::StreamEnd;
            Ok(out)
        }
    }
}

impl Drop for Parser {
    fn drop(&mut self) {
        // SAFETY: the pointer comes from `Box::into_raw` in `new` and is freed only here.
        unsafe {
            let p = self.pin.as_ptr();
            unsafe_sys::yaml_parser_delete(std::ptr::addr_of_mut!((*p).sys));
            drop(Box::from_raw(p));
        }
    }
}

/// Whether the parser has recorded a problem.
///
/// # Safety
/// `parser` is an initialized parser.
unsafe fn failed(parser: *const unsafe_sys::yaml_parser_t) -> bool {
    // SAFETY: as the caller says.
    unsafe { (&*parser).error != unsafe_sys::YAML_NO_ERROR }
}

/// A NUL-terminated text of the library, if any.
///
/// # Safety
/// `p` is null or points to a NUL-terminated string.
unsafe fn text(p: *const u8) -> Option<String> {
    if p.is_null() {
        return None;
    }
    // SAFETY: as the caller says.
    Some(unsafe { CStr::from_ptr(p.cast()) }.to_string_lossy().into_owned())
}

/// # Safety
/// `parser` is an initialized parser that failed.
unsafe fn error_of(parser: *const unsafe_sys::yaml_parser_t) -> Error {
    // SAFETY: the fields of the prefix are read from an initialized parser.
    unsafe {
        let p = &**parser;
        Error {
            kind: match p.error {
                unsafe_sys::YAML_MEMORY_ERROR => ErrorKind::Memory,
                unsafe_sys::YAML_READER_ERROR => ErrorKind::Reader,
                unsafe_sys::YAML_SCANNER_ERROR => ErrorKind::Scanner,
                unsafe_sys::YAML_PARSER_ERROR => ErrorKind::Parser,
                _ => ErrorKind::Other,
            },
            problem: text(p.problem.cast()).unwrap_or_default(),
            problem_offset: p.problem_offset,
            problem_mark: p.problem_mark.into(),
            context: text(p.context.cast()),
            context_mark: p.context_mark.into(),
        }
    }
}

/// # Safety
/// `ev` is an event the parser filled.
unsafe fn kind_of(ev: &unsafe_sys::yaml_event_t) -> EventKind {
    // SAFETY: the union field read is the one the event's type names.
    unsafe {
        match ev.type_ {
            unsafe_sys::YAML_STREAM_START_EVENT => EventKind::StreamStart,
            unsafe_sys::YAML_STREAM_END_EVENT => EventKind::StreamEnd,
            unsafe_sys::YAML_DOCUMENT_START_EVENT => EventKind::DocumentStart,
            unsafe_sys::YAML_DOCUMENT_END_EVENT => EventKind::DocumentEnd,
            unsafe_sys::YAML_ALIAS_EVENT => EventKind::Alias {
                anchor: text(ev.data.alias.anchor).unwrap_or_default(),
            },
            unsafe_sys::YAML_SCALAR_EVENT => {
                let s = ev.data.scalar;
                let bytes = std::slice::from_raw_parts(s.value, s.length as usize);
                EventKind::Scalar {
                    anchor: text(s.anchor),
                    tag: text(s.tag),
                    value: String::from_utf8_lossy(bytes).into_owned(),
                    style: match s.style {
                        unsafe_sys::YAML_SINGLE_QUOTED_SCALAR_STYLE => ScalarStyle::SingleQuoted,
                        unsafe_sys::YAML_DOUBLE_QUOTED_SCALAR_STYLE => ScalarStyle::DoubleQuoted,
                        unsafe_sys::YAML_LITERAL_SCALAR_STYLE => ScalarStyle::Literal,
                        unsafe_sys::YAML_FOLDED_SCALAR_STYLE => ScalarStyle::Folded,
                        _ => ScalarStyle::Plain,
                    },
                }
            }
            unsafe_sys::YAML_SEQUENCE_START_EVENT => {
                let s = ev.data.sequence_start;
                EventKind::SequenceStart {
                    anchor: text(s.anchor),
                    tag: text(s.tag),
                    flow: s.style == unsafe_sys::YAML_FLOW_SEQUENCE_STYLE,
                }
            }
            unsafe_sys::YAML_SEQUENCE_END_EVENT => EventKind::SequenceEnd,
            unsafe_sys::YAML_MAPPING_START_EVENT => {
                let m = ev.data.mapping_start;
                EventKind::MappingStart {
                    anchor: text(m.anchor),
                    tag: text(m.tag),
                    flow: m.style == unsafe_sys::YAML_FLOW_MAPPING_STYLE,
                }
            }
            unsafe_sys::YAML_MAPPING_END_EVENT => EventKind::MappingEnd,
            _ => EventKind::StreamEnd,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn events(src: &str) -> Result<Vec<EventKind>, Error> {
        let mut p = Parser::new(src.as_bytes());
        let mut out = Vec::new();
        loop {
            let e = p.next_event()?;
            let end = e.kind == EventKind::StreamEnd;
            out.push(e.kind);
            if end {
                return Ok(out);
            }
        }
    }

    #[test]
    fn reads_a_document() {
        let ev = events("a: 'b'\nc: [1, &x d, *x]\n").unwrap();
        assert!(ev.contains(&EventKind::Scalar {
            anchor: None,
            tag: None,
            value: "b".into(),
            style: ScalarStyle::SingleQuoted
        }));
        assert!(ev.contains(&EventKind::Alias { anchor: "x".into() }));
    }

    #[test]
    fn says_the_problem() {
        let e = events("a: b\n c: d\n").unwrap_err();
        assert_eq!(e.kind, ErrorKind::Scanner);
        assert_eq!(e.problem, "mapping values are not allowed in this context");
    }
}
