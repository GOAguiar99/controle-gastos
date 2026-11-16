-- A recurrence suggested by the app (e.g. the installments of a new loan) waits
-- for the user to accept it; until then it is inactive and not projected.
ALTER TABLE recurrences ADD COLUMN pending INTEGER NOT NULL DEFAULT 0;
