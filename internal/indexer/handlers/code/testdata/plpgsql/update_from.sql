CREATE FUNCTION public.update_from() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  UPDATE orders SET status = 'x' FROM users WHERE orders.user_id = users.id;
END;
$$;
