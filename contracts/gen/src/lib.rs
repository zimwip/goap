//! The generated contracts (ADR 0125): `goap.<package>.v1` messages with serde in the protobuf JSON form.
#![allow(clippy::all, missing_docs)]

/// The encoded `FileDescriptorSet` of proto/.
pub const DESCRIPTOR: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/descriptor.bin"));

pub mod assistant {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.assistant.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.assistant.v1.serde.rs"));
    }
}
pub mod connector {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.connector.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.connector.v1.serde.rs"));
    }
}
pub mod conversations {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.conversations.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.conversations.v1.serde.rs"));
    }
}
pub mod credentials {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.credentials.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.credentials.v1.serde.rs"));
    }
}
pub mod engine {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.engine.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.engine.v1.serde.rs"));
    }
}
pub mod events {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.events.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.events.v1.serde.rs"));
    }
}
pub mod graph {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.graph.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.graph.v1.serde.rs"));
    }
}
pub mod index {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.index.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.index.v1.serde.rs"));
    }
}
pub mod mcp {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.mcp.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.mcp.v1.serde.rs"));
    }
}
pub mod model {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.model.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.model.v1.serde.rs"));
    }
}
pub mod preferences {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.preferences.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.preferences.v1.serde.rs"));
    }
}
pub mod registry {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.registry.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.registry.v1.serde.rs"));
    }
}
pub mod runtime {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/goap.runtime.v1.rs"));
        include!(concat!(env!("OUT_DIR"), "/goap.runtime.v1.serde.rs"));
    }
}
