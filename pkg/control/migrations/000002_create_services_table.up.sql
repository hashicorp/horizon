CREATE TABLE IF NOT EXISTS services (
  id serial PRIMARY KEY,
  service_id bytea NOT NULL UNIQUE,
  created_at timestamp NOT NULL DEFAULT now(),
  updated_at timestamp NOT NULL DEFAULT now(),
  hub_id bytea NOT NULL,
  account_id bytea NOT NULL,
  type text NOT NULL,
  description text NOT NULL,
  labels text[] NOT NULL
);

CREATE INDEX account_services ON services USING btree (account_id, id);
