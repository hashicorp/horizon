-- Copyright (c) HashiCorp, Inc.
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE IF NOT EXISTS accounts (
  id bytea PRIMARY KEY,
  namespace text NOT NULL,

  created_at timestamp NOT NULL DEFAULT now(),
  updated_at timestamp NOT NULL DEFAULT now()
);
