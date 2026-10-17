//! Port of `events_test.go`.
use std::path::Path;

use regex::Regex;

fn web(path: &str) -> String {
    let p = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .join(path);
    std::fs::read_to_string(&p).unwrap_or_else(|e| panic!("{}: {e}", p.display()))
}

// The web keeps one copy of the trigger events (TRIGGER_EVENTS) and labels them (web/src/lib/subscriptions.ts, the
// on and trigger editors): both must list what the Rust side lists.
#[test]
fn web_lists_the_trigger_events() {
    let mut want: Vec<String> = goap_events::ALL.iter().map(|s| s.to_string()).collect();
    want.sort();

    let api = web("web/src/lib/api/types/registry.ts");
    let block = Regex::new(r"(?s)TRIGGER_EVENTS = \[(.*?)\] as const")
        .unwrap()
        .captures(&api)
        .expect("TRIGGER_EVENTS not found in web/src/lib/api/types/registry.ts");
    let quoted = Regex::new(r"'([^']+)'").unwrap();
    let mut got: Vec<String> = quoted
        .captures_iter(&block[1])
        .map(|m| m[1].to_string())
        .collect();
    got.sort();
    assert_eq!(
        got, want,
        "web TRIGGER_EVENTS {got:?}, events::ALL {want:?}"
    );

    let editor = web("web/src/lib/subscriptions.ts");
    let labels = Regex::new(r"(?s)EVENT_LABELS: Record<string, string> = \{(.*?)\};")
        .unwrap()
        .captures(&editor)
        .expect("EVENT_LABELS not found in web/src/lib/subscriptions.ts");
    let key = Regex::new(r"(?m)^\s*'([^']+)':").unwrap();
    let mut got: Vec<String> = key
        .captures_iter(&labels[1])
        .map(|m| m[1].to_string())
        .collect();
    got.sort();
    assert_eq!(got, want, "web EVENT_LABELS {got:?}, events::ALL {want:?}");
}

#[test]
fn from_broker() {
    for (name, want) in [
        (goap_events::BROKER_ATTACHED, goap_events::PROCESS_ATTACHED),
        (
            goap_events::BROKER_STEP_COMPLETED,
            goap_events::STEP_COMPLETED,
        ),
    ] {
        assert_eq!(
            goap_events::from_broker(name),
            Some(want),
            "from_broker({name:?})"
        );
    }
    assert!(
        goap_events::from_broker("completed").is_none(),
        "a process status is not a broker alias"
    );
    assert_eq!(
        goap_events::change_subject("C1", goap_events::CHANGE_ITEM_ADDED),
        "goap.change.C1.item_added"
    );
}
