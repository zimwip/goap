// Minimal client for the GOAP gateway (Connect protocol, JSON encoding).
// Each RPC is a `POST /{package.Service}/{Method}` with a JSON body
// (proto3 JSON: fields in lowerCamelCase, default values omitted).
//
// The client is split in `./api/` (ADR 0073): the transport at the bottom (`transport.ts`), the proto3 JSON shapes
// (`types/*.ts`, no runtime but two constants), one module per service client, the session calls (`authApi.ts`) and
// the pure helpers. This file is the one import of the web (`from '../api'`): it re-exports them all.

export * from './api/transport';
export type { Int64, JsonValue, Struct } from './api/types/common';
export * from './api/types/registry';
export * from './api/types/access';
export * from './api/types/graph';
export * from './api/types/engine';
export * from './api/types/models';
export * from './api/types/mcp';
export * from './api/types/nodeIndex';
export * from './api/registry';
export * from './api/authApi';
export * from './api/graph';
export * from './api/engine';
export * from './api/status';
export * from './api/helpers';
export * from './api/models';
export * from './api/mcp';
export * from './api/nodeIndex';
