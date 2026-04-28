CREATE FUNCTION public.execute_nested() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE 'EXECUTE ''SELECT 1''';
END;
$$;
