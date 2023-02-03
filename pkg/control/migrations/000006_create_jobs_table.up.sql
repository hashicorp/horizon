-- Copyright (c) HashiCorp, Inc.
-- SPDX-License-Identifier: MPL-2.0

CREATE TYPE job_status AS ENUM ('queued', 'finished');

CREATE TABLE IF NOT EXISTS jobs (
  id bytea PRIMARY KEY,
  queue text NOT NULL,
  status job_status NOT NULL DEFAULT 'queued',
  job_type text NOT NULL,
  payload jsonb,
  cool_off_until timestamp with time zone,
  attempts int NOT NULL DEFAULT 0,
  created_at timestamp with time zone NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS periodic_jobs (
  id serial PRIMARY KEY,
  name text NOT NULL UNIQUE,
  queue text NOT NULL,
  job_type text NOT NULL,
  payload jsonb,
  period text NOT NULL DEFAULT '1h',
  next_run timestamp with time zone NOT NULL DEFAULT now(),
  created_at timestamp with time zone NOT NULL DEFAULT now()
)
