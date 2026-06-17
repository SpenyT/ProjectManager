CREATE TABLE app_user (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email      TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX app_user_email_lower_uq ON app_user (LOWER(email));

CREATE TABLE team (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE team_membership (
  team_id    UUID NOT NULL REFERENCES team(id) ON DELETE CASCADE,
  user_id    UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  role       TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_membership_user_idx ON team_membership (user_id);

CREATE TABLE inventory_scope (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  team_id    UUID NOT NULL REFERENCES team(id) ON DELETE CASCADE,
  name       TEXT NOT NULL DEFAULT 'default',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX inventory_scope_team_idx ON inventory_scope (team_id);
