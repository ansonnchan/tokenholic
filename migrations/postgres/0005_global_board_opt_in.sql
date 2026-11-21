-- Global-board participation is opt-in. The column defaults to false, so no
-- existing or new account is listed until its owner chooses to be, and the
-- global board reads only rows where it is true.
ALTER TABLE users ADD COLUMN global_opt_in BOOLEAN NOT NULL DEFAULT FALSE;

-- The global board starts from its participants, a small share of accounts.
CREATE INDEX users_global_opt_in ON users(id) WHERE global_opt_in AND deleted_at IS NULL;

COMMENT ON COLUMN users.global_opt_in IS 'True only after the account owner joined the global board. Listing an account anywhere outside its own groups requires it.';
