-- The working version of a change (ADR 0076): written by CreateNode or CheckoutNode, edited in place until its check-in.
ALTER TABLE node_version ADD COLUMN checked_out boolean NOT NULL DEFAULT false;
