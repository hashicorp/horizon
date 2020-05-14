CREATE TABLE IF NOT EXISTS services (
  id serial PRIMARY KEY,
  service_id bytea NOT NULL,
  hub_id bytea NOT NULL,
  account_id bytea NOT NULL,
  type text NOT NULL,
  description text NOT NULL,
  labels text[] NOT NULL,
  created_at timestamp NOT NULL DEFAULT now(),
  updated_at timestamp NOT NULL DEFAULT now()
);

CREATE INDEX account_services ON services USING btree (account_id, id);
CREATE INDEX service_by_service_id ON services USING (service_id);
