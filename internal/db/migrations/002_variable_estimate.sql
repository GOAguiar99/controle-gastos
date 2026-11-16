-- Estimated monthly variable spending (cartão + outros) used by the cash-flow
-- projection. -1 = automatic (average of the last 3 closed months), 0 = off.
INSERT OR IGNORE INTO settings (key, value) VALUES ('variable_estimate_cents', '-1');
