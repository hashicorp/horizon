CREATE TABLE IF NOT EXISTS agents (
  id bytea PRIMARY KEY,
  account_id bytea NOT NULL,
  created_at timestamp NOT NULL DEFAULT now(),
  updated_at timestamp NOT NULL DEFAULT now()
);
