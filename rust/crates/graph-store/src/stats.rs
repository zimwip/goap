//! What the statements of a request cost: counted while a future runs inside [`counted`], read when it ends (the
//! requests of the graph service, logged at debug under `goap::rpc`, with the statements themselves at trace). A
//! statement run outside is not counted.
use std::cell::RefCell;

/// The round trips of a request to its database, the rows they returned or touched, and, when asked, the head of
/// each statement.
#[derive(Debug, Default, Clone)]
pub struct Counts {
    pub statements: u32,
    pub rows: u64,
    /// The first words of each statement, in order (only when [`counted`] was asked for them).
    pub texts: Option<Vec<String>>,
}

tokio::task_local! {
    static COUNTS: RefCell<Counts>;
}

/// Runs a future, counting the statements it runs (and noting their heads when `texts`).
pub async fn counted<F: std::future::Future>(f: F, texts: bool) -> (F::Output, Counts) {
    let start = Counts {
        texts: texts.then(Vec::new),
        ..Counts::default()
    };
    COUNTS
        .scope(RefCell::new(start), async move {
            let out = f.await;
            (out, COUNTS.with(|c| c.take()))
        })
        .await
}

/// Whether the request running asks for the heads of its statements.
pub(crate) fn texts() -> bool {
    COUNTS.try_with(|c| c.borrow().texts.is_some()).unwrap_or(false)
}

/// Records a statement and the rows it returned or touched.
pub(crate) fn note(text: &str, rows: u64) {
    let _ = COUNTS.try_with(|c| {
        let mut n = c.borrow_mut();
        n.statements += 1;
        n.rows += rows;
        if let Some(t) = &mut n.texts {
            t.push(
                text.split_whitespace()
                    .collect::<Vec<_>>()
                    .join(" ")
                    .chars()
                    .take(140)
                    .collect(),
            );
        }
    });
}
