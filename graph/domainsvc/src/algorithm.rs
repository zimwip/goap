//! Port of `algorithm.go`: trying an algorithm on a sample input.

use goap_algo::{Algorithm, Bound};
use goap_ctx::Ctx;
use goap_def::Domain;
use goap_dsl::{self as dsl, AlgorithmInput, ChangeInfo, LinkInfo, Node, Outcome, TransitionInfo};
use serde::Deserialize;
use serde_json::{Map, Value};

use crate::Service;
use crate::error::Error;

/// The sample input of a try: the fields of every usage (only those of the algorithm's are read).
#[derive(Default, Deserialize)]
#[serde(default)]
struct Sample {
    node: Node,
    property: String,
    value: Value,
    link: LinkInfo,
    from: Node,
    to: Node,
    children: Vec<Node>,
    transition: TransitionInfo,
    change: ChangeInfo,
}

impl Service {
    /// RunAlgorithm tries an algorithm on a sample input (nothing is stored). It needs the right to edit domains.
    pub async fn run_algorithm(
        &self,
        ctx: &Ctx,
        a: &Algorithm,
        values: &Map<String, Value>,
        input: &Map<String, Value>,
    ) -> Result<Outcome, Error> {
        self.authorize_domain(ctx, "write", &Domain::default())
            .await?;
        if let Some(i) = a.issues().first() {
            return Err(Error::invalid_because(i));
        }
        dsl::check_algorithm_code(&a.language, &a.code).map_err(Error::invalid_because)?;
        let (params, issues) = a.resolve(values);
        if let Some(i) = issues.first() {
            return Err(Error::invalid_because(i));
        }
        let sample: Sample = if input.is_empty() {
            Sample::default()
        } else {
            serde_json::from_value(Value::Object(input.clone()))
                .map_err(|e| Error::invalid_because(format!("sample input: {e}")))?
        };
        let b = Bound {
            instance: "try".into(),
            algorithm: a.name.clone(),
            r#type: a.r#type.clone(),
            language: a.language.clone(),
            code: a.code.clone(),
            params,
            ..Bound::default()
        };
        let input = AlgorithmInput {
            node: sample.node,
            property: sample.property,
            value: sample.value,
            link: sample.link,
            from: sample.from,
            to: sample.to,
            children: sample.children,
            change: sample.change,
            transition: sample.transition,
            ..AlgorithmInput::default()
        };
        Ok(dsl::run_algorithm(ctx, &b, input).await?)
    }
}
