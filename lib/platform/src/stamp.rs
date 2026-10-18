//! Who issued the request being served, and the id of its command (ADR 0053).
use goap_ctx::Ctx;

/// Stamp says who issued the request being served and the client-generated id of its command (ADR 0053). It rides
/// the context of a service, and the bus carries it as message headers, so the events a write causes name both.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Stamp {
    pub actor: String,
    pub command: String,
}

/// Message header carrying the actor of a [`Stamp`] on the bus.
pub const HEADER_ACTOR: &str = "X-Goap-Actor";
/// Message header carrying the command of a [`Stamp`] on the bus.
pub const HEADER_COMMAND: &str = "X-Goap-Command";

/// Returns ctx carrying the stamp.
pub fn with_stamp(ctx: &Ctx, s: Stamp) -> Ctx {
    ctx.with(s)
}

/// Returns the stamp of ctx (zero when none).
pub fn stamp_of(ctx: &Ctx) -> Stamp {
    ctx.value::<Stamp>().cloned().unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stamp_rides_the_context() {
        let ctx = Ctx::background();
        assert_eq!(stamp_of(&ctx), Stamp::default());
        let s = Stamp {
            actor: "alice".into(),
            command: "c1".into(),
        };
        assert_eq!(stamp_of(&with_stamp(&ctx, s.clone()).with(1u8)), s);
    }
}
