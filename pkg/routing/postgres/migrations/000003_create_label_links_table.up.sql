CREATE TABLE IF NOT EXISTS label_links (
  id serial PRIMARY KEY,
  account_id bytea NOT NULL,
  labels text[] NOT NULL,
  target text[] NOT NULL,

  created_at timestamp NOT NULL DEFAULT now(),
  updated_at timestamp NOT NULL DEFAULT now(),

  UNIQUE(account_id, labels)
)
