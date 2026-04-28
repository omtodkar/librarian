CREATE FUNCTION public.execute_concat_mixed(tname text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE 'SELECT * FROM ' || tname || ' WHERE id = 1';
END;
$$;
