CREATE TABLE IF NOT EXISTS services (
  id bytea PRIMARY KEY,
  created_at timestamp NOT NULL DEFAULT now(),
  updated_at timestamp NOT NULL DEFAULT now(),
  agent_id bytea REFERENCES agents(id),
  type text NOT NULL,
  description text NOT NULL,
  labels text[] NOT NULL
)
