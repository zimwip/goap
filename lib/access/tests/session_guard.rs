//! Port of session_guard_test.go.

use std::path::Path;

use regex::Regex;

fn walk(dir: &Path, out: &mut Vec<std::path::PathBuf>) {
    let mut entries: Vec<_> = std::fs::read_dir(dir)
        .unwrap()
        .filter_map(|e| e.ok())
        .collect();
    entries.sort_by_key(|e| e.path());
    for e in entries {
        let p = e.path();
        if p.is_dir() {
            walk(&p, out);
        } else {
            out.push(p);
        }
    }
}

/// The web learns the names of the organisation, how its keys are built and what the caller may attempt from the
/// session (ADR 0070), in one module. These patterns must not come back anywhere else.
#[test]
fn web_mirrors_nothing_of_the_session() {
    let forbidden: Vec<(&str, Regex)> = [
        ("a hand-built user key", r#"['"`]USR:"#),
        ("a hand-built assignment key", r#"['"`]ASG:"#),
        ("a hand-built policy key", r#"['"`]POL:"#),
        ("a root key", r#"['"`](ORG-DEFAULT|PROJ-ROOT)['"`]"#),
        ("a role-name gate", r#"hasAnyRole\(\s*['"]"#),
        ("a qualified type of the organisation", r"organisation@\w"),
        ("a local namespace constant", r#"const NS(_\w+)? = ['"]"#),
    ]
    .into_iter()
    .map(|(what, re)| (what, Regex::new(re).unwrap()))
    .collect();
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../web/src");
    let mut files = Vec::new();
    walk(&root, &mut files);
    let mut errors = Vec::new();
    for path in files {
        let slash = path.to_string_lossy().replace('\\', "/");
        if !(slash.ends_with(".ts") || slash.ends_with(".svelte"))
            || slash.ends_with(".test.ts")
            || slash.ends_with("/lib/stores/session.svelte.ts")
            || slash.contains("/lib/help/")
        {
            continue;
        }
        let src = std::fs::read_to_string(&path).unwrap();
        for (line_no, line) in src.split('\n').enumerate() {
            if let Some(c) = line.find("//")
                && line[..c].trim().is_empty()
            {
                continue; // a comment line may name them
            }
            for (what, re) in &forbidden {
                if re.is_match(line) {
                    errors.push(format!(
                        "{slash}:{}: {what}: ask the session module (stores/session.svelte.ts) instead: {}",
                        line_no + 1,
                        line.trim()
                    ));
                }
            }
        }
    }
    assert!(errors.is_empty(), "{}", errors.join("\n"));
}
