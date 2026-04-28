CREATE FUNCTION public.while_loop() RETURNS void LANGUAGE plpgsql AS $$
DECLARE cnt int := 0;
BEGIN
  WHILE cnt < 5 LOOP
    DELETE FROM events WHERE n = cnt;
    cnt := cnt + 1;
  END LOOP;
END;
$$;
