-- Copyright (c) HashiCorp, Inc.
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE IF NOT EXISTS hubs (
  stable_id bytea PRIMARY KEY,
  instance_id bytea UNIQUE,
  connection_info jsonb,
  last_checkin timestamp with time zone NOT NULL,
  created_at timestamp with time zone NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS hub_instance_id ON hubs (instance_id);
