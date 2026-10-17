//! Package startingpoint composes the possible steps of a change (ADR 0097) for their readers: the engine binds the
//! steps of the compiled view of the methodology to the change (engine.v1.ListStartingPoints: which step, the method
//! that applies, why it is possible, how to start it, whether the caller may), the view says what each step is
//! (registry.v1.MethodologyView: name, description, guidance, exit, roles). Compose puts the two together, by the path
//! of the step (ADR 0126 step 5). It is pure and names neither the engine nor the registry: the goap-scheduler tool and
//! the assistant call it with what the two services answered, the web does the same in web/src/lib/startingPoints.ts.

mod compose;

pub use compose::*;
