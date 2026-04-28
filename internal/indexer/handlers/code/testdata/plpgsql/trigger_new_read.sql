CREATE FUNCTION public.trigger_new_read() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO audit(user_id, email) VALUES (NEW.user_id, NEW.email);
  RETURN NEW;
END;
$$;
