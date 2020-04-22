CREATE TYPE job_status AS ENUM ('queued', 'finished');

CREATE TABLE IF NOT EXISTS jobs (
  id bytea PRIMARY KEY,
  queue text NOT NULL,
  status job_status NOT NULL DEFAULT 'queued',
  job_type text NOT NULL,
  payload jsonb,
  created_at timestamp NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS periodic_jobs (
  id serial PRIMARY KEY,
  name text NOT NULL UNIQUE,
  queue text NOT NULL,
  job_type text NOT NULL,
  payload jsonb,
  period text NOT NULL DEFAULT '1h',
  next_run timestamp NOT NULL DEFAULT now(),
  created_at timestamp NOT NULL DEFAULT now()
)
