CREATE FUNCTION public.execute_variable(tname text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE 'DELETE FROM ' || tname;
END;
$$;
