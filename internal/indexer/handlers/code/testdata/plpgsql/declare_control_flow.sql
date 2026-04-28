CREATE FUNCTION public.declare_control_flow(uid int) RETURNS int LANGUAGE plpgsql AS $$
DECLARE
  cnt int;
BEGIN
  SELECT count(*) INTO cnt FROM orders WHERE user_id = uid;
  IF cnt > 0 THEN
    RAISE NOTICE 'has orders';
  END IF;
  RETURN cnt;
END;
$$;
