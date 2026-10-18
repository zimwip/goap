// Code generated from the widget registry of pkg/form (GOAP_UPDATE_WEB=1 go test ./pkg/form/). DO NOT EDIT.
import type { WidgetSpec } from './types';

export const WIDGET_CATALOG: WidgetSpec[] = [
  {
    "name": "boolean",
    "description": "yes or no",
    "value": "boolean"
  },
  {
    "name": "date",
    "description": "a date, YYYY-MM-DD",
    "value": "string",
    "constraints": [
      {
        "name": "min",
        "type": "string",
        "description": "earliest date, YYYY-MM-DD"
      },
      {
        "name": "max",
        "type": "string",
        "description": "latest date, YYYY-MM-DD"
      }
    ]
  },
  {
    "name": "decision",
    "description": "rules a decision point of the change: an option and why",
    "value": "object",
    "constraints": [
      {
        "name": "justify",
        "type": "boolean",
        "description": "the justification is required"
      },
      {
        "name": "roles",
        "type": "strings",
        "description": "the seats of a collegial decision: the person rules in one of these roles (ADR 0132)"
      }
    ]
  },
  {
    "name": "file",
    "description": "a file the person attaches, kept in the values",
    "value": "object",
    "constraints": [
      {
        "name": "accept",
        "type": "strings",
        "description": "media types (text/*, application/pdf) or extensions (.csv) accepted"
      },
      {
        "name": "maxBytes",
        "type": "number",
        "description": "largest file, in bytes (at most 524288)"
      },
      {
        "name": "multiple",
        "type": "boolean",
        "description": "several files"
      },
      {
        "name": "min",
        "type": "number",
        "description": "fewest files (multiple)"
      },
      {
        "name": "max",
        "type": "number",
        "description": "most files (multiple)"
      }
    ]
  },
  {
    "name": "group",
    "description": "entries repeated by the person, each made of the sub-fields",
    "value": "objects",
    "constraints": [
      {
        "name": "min",
        "type": "number",
        "description": "fewest entries"
      },
      {
        "name": "max",
        "type": "number",
        "description": "most entries"
      }
    ]
  },
  {
    "name": "link",
    "description": "links to create from a node to nodes found by a search",
    "value": "links",
    "constraints": [
      {
        "name": "from",
        "type": "field",
        "description": "the node field (or new node field) whose node is the source",
        "widgets": [
          "node",
          "newNode"
        ]
      },
      {
        "name": "fromKey",
        "type": "string",
        "description": "a fixed source node"
      },
      {
        "name": "linkTypes",
        "type": "strings",
        "description": "qualified link types offered"
      },
      {
        "name": "toTypes",
        "type": "strings",
        "description": "qualified node types of the targets (a subtype qualifies)"
      },
      {
        "name": "toStates",
        "type": "strings",
        "description": "lifecycle states of the targets"
      },
      {
        "name": "min",
        "type": "number",
        "description": "fewest links"
      },
      {
        "name": "max",
        "type": "number",
        "description": "most links"
      }
    ]
  },
  {
    "name": "markdown",
    "description": "a text shown to the person",
    "value": "none",
    "constraints": [
      {
        "name": "text",
        "type": "string",
        "description": "the Markdown text",
        "required": true
      }
    ]
  },
  {
    "name": "multiselect",
    "description": "values among options",
    "value": "list",
    "constraints": [
      {
        "name": "options",
        "type": "strings",
        "description": "the values offered",
        "required": true
      },
      {
        "name": "min",
        "type": "number",
        "description": "fewest values"
      },
      {
        "name": "max",
        "type": "number",
        "description": "most values"
      }
    ]
  },
  {
    "name": "newNode",
    "description": "a node the change creates: its type, key and properties",
    "value": "object",
    "constraints": [
      {
        "name": "types",
        "type": "strings",
        "description": "qualified node types the person may create (a subtype qualifies)"
      },
      {
        "name": "properties",
        "type": "strings",
        "description": "the attributes asked (none: the name of the type)"
      },
      {
        "name": "multiple",
        "type": "boolean",
        "description": "several nodes"
      },
      {
        "name": "min",
        "type": "number",
        "description": "fewest nodes (multiple)"
      },
      {
        "name": "max",
        "type": "number",
        "description": "most nodes (multiple)"
      }
    ]
  },
  {
    "name": "node",
    "description": "a node of the graph, found by a search",
    "value": "node",
    "constraints": [
      {
        "name": "types",
        "type": "strings",
        "description": "qualified node types offered (a subtype qualifies)"
      },
      {
        "name": "states",
        "type": "strings",
        "description": "lifecycle states offered"
      },
      {
        "name": "namespace",
        "type": "string",
        "description": "namespace of the nodes offered"
      },
      {
        "name": "inChange",
        "type": "boolean",
        "description": "only the nodes the change touches"
      },
      {
        "name": "multiple",
        "type": "boolean",
        "description": "several nodes"
      },
      {
        "name": "min",
        "type": "number",
        "description": "fewest nodes (multiple)"
      },
      {
        "name": "max",
        "type": "number",
        "description": "most nodes (multiple)"
      }
    ]
  },
  {
    "name": "number",
    "description": "a number",
    "value": "number",
    "constraints": [
      {
        "name": "min",
        "type": "number"
      },
      {
        "name": "max",
        "type": "number"
      },
      {
        "name": "step",
        "type": "number"
      },
      {
        "name": "integer",
        "type": "boolean",
        "description": "whole numbers only"
      }
    ]
  },
  {
    "name": "select",
    "description": "one value among options",
    "value": "string",
    "constraints": [
      {
        "name": "options",
        "type": "strings",
        "description": "the values offered",
        "required": true
      }
    ]
  },
  {
    "name": "text",
    "description": "a line of text",
    "value": "string",
    "constraints": [
      {
        "name": "minLength",
        "type": "number",
        "description": "fewest characters"
      },
      {
        "name": "maxLength",
        "type": "number",
        "description": "most characters"
      },
      {
        "name": "pattern",
        "type": "string",
        "description": "regular expression the whole text matches"
      }
    ]
  },
  {
    "name": "textarea",
    "description": "a text of several lines",
    "value": "string",
    "constraints": [
      {
        "name": "minLength",
        "type": "number",
        "description": "fewest characters"
      },
      {
        "name": "maxLength",
        "type": "number",
        "description": "most characters"
      },
      {
        "name": "pattern",
        "type": "string",
        "description": "regular expression the whole text matches"
      }
    ]
  }
];
