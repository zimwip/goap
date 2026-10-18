//! The OKF rendering of a snapshot (the body of `ExportGraphOkf`, ADR 0111).

use std::sync::Arc;

use goap_graph::Snapshot;
use goap_okf::Options;

/// render writes the OKF archive of a snapshot into memory, its nodes named and laid out by the type catalogue when
/// one is given, the defaults of the writer otherwise.
pub(crate) fn render(
    types: Option<Arc<goap_typecat::Catalog>>,
    s: &Snapshot,
) -> Result<Vec<u8>, goap_okf::Error> {
    let o = match types {
        Some(c) => goap_okf::from_catalog(c),
        None => Options::default(),
    };
    let mut out = std::io::Cursor::new(Vec::new());
    goap_okf::write(&mut out, s, &o, s.exported_at)?;
    Ok(out.into_inner())
}
