CREATE TABLE IF NOT EXISTS agents (
  id bigserial PRIMARY KEY,
  created_at timestamp NOT NULL DEFAULT now(),
  updated_at timestamp NOT NULL DEFAULT now(),
  session_id bytea NOT NULL
);
