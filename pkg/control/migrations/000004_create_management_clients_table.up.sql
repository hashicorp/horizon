-- Copyright (c) HashiCorp, Inc.
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE IF NOT EXISTS management_clients (
  id bytea PRIMARY KEY,
  namespace text NOT NULL
);
