CREATE TABLE IF NOT EXISTS services (
  created_at timestamp NOT NULL DEFAULT now(),
  updated_at timestamp NOT NULL DEFAULT now(),
  agent_id integer REFERENCES agents(id),
  service_id bytea NOT NULL,
  type text NOT NULL,
  description text NOT NULL,
  labels text[] NOT NULL
)
