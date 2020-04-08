CREATE TABLE IF NOT EXISTS hostnames (
  id serial PRIMARY KEY,

  account_id bytea NOT NULL,
  name text NOT NULL UNIQUE,

  created_at timestamp NOT NULL DEFAULT now(),
  updated_at timestamp NOT NULL DEFAULT now()
);

CREATE EXTENSION bloom;

CREATE INDEX IF NOT EXISTS hostname_names ON hostnames USING bloom (name);
