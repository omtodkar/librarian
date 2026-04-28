CREATE FUNCTION public.for_loop() RETURNS void LANGUAGE plpgsql AS $$
DECLARE r RECORD;
BEGIN
  FOR r IN SELECT id, name FROM users LOOP
    UPDATE orders SET user_id = r.id WHERE user_name = r.name;
  END LOOP;
END;
$$;
