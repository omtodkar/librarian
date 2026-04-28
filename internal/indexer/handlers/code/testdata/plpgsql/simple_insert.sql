CREATE FUNCTION public.simple_insert() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO users(name) VALUES ('a');
END;
$$;
