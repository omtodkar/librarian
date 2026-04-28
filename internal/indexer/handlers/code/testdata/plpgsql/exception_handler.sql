CREATE FUNCTION public.exception_handler() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO t VALUES (1);
EXCEPTION WHEN unique_violation THEN
  INSERT INTO dup_log VALUES (1);
END;
$$;
