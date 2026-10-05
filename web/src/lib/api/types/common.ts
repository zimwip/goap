// Shapes shared by every service (proto3 JSON).

export type JsonValue = null | boolean | number | string | JsonValue[] | { [k: string]: JsonValue };
export type Struct = { [k: string]: JsonValue };
export type Empty = Record<string, never>;

/** 64-bit integer: proto3 JSON serializes it as a string. */
export type Int64 = number | string;
