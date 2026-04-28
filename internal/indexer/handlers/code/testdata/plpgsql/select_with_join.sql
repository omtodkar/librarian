CREATE FUNCTION public.select_with_join() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  SELECT u.id FROM users u JOIN orders o ON o.user_id = u.id;
END;
$$;
