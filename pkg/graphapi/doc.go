// Package graphapi is the contract of the graph for its callers: the requests and answers of the operations on a change
// (engine.GraphPort, the built-in MCPs, the services) and the errors they return. It depends on pkg/domain only, so a
// caller can name the graph's operations without importing the graph and its storage (pkg/layering).
package graphapi
