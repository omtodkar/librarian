CREATE FUNCTION public.execute_literal() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE 'INSERT INTO audit VALUES (1)';
END;
$$;
