CREATE TABLE IF NOT EXISTS hubs (
  id bytea PRIMARY KEY,
  connection_info jsonb,
  last_checkin timestamp with time zone NOT NULL,
  created_at timestamp with time zone NOT NULL DEFAULT now()
);
