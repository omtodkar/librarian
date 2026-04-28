CREATE FUNCTION public.delete_using() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM orders USING users WHERE orders.user_id = users.id AND users.banned;
END;
$$;
