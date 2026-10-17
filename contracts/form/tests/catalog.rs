//! Port of `catalog_test.go`.
use std::path::Path;

/// The copy of the built-in widgets the form designer of the web reads (ADR 0115): their values and constraints,
/// written from the registry so that both stay the same.
const WEB_CATALOG: &str = "../../web/src/lib/forms/widgetCatalog.gen.ts";

#[test]
fn web_catalog_is_current() {
    let raw = serde_json::to_string_pretty(&goap_form::all()).unwrap();
    let want = "// Code generated from the widget registry of pkg/form (GOAP_UPDATE_WEB=1 go test ./pkg/form/). DO NOT EDIT.\n"
        .to_string()
        + "import type { WidgetSpec } from './types';\n\n"
        + "export const WIDGET_CATALOG: WidgetSpec[] = "
        + &raw
        + ";\n";
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(WEB_CATALOG);
    if std::env::var("GOAP_UPDATE_WEB").is_ok_and(|v| !v.is_empty()) {
        std::fs::write(&path, want).unwrap();
        return;
    }
    let got = std::fs::read_to_string(&path).unwrap();
    assert!(
        got == want,
        "{WEB_CATALOG} is not the registry's: run GOAP_UPDATE_WEB=1 cargo test -p goap-form"
    );
}
