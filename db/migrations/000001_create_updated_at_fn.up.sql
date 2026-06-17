CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
  IF row(NEW.*) IS DISTINCT FROM row(OLD.*) THEN
    NEW.updated_at := now();
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
