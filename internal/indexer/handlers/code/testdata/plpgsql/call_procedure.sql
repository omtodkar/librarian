CREATE FUNCTION public.call_procedure() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  CALL my_procedure(1, 2);
END;
$$;
