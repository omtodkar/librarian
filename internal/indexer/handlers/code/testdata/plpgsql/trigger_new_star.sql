CREATE FUNCTION public.trigger_new_star() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO audit SELECT NEW.*;
  RETURN NEW;
END;
$$;
