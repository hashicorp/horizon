-- Copyright (c) HashiCorp, Inc.
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE IF NOT EXISTS activity_logs (
  id bigserial PRIMARY KEY,
  event jsonb NOT NULL,
  created_at timestamp(6) with time zone NOT NULL DEFAULT now()
);
