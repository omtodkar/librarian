CREATE FUNCTION public.case_expression() RETURNS void LANGUAGE plpgsql AS $$
DECLARE x int := 1;
BEGIN
  CASE x
    WHEN 1 THEN INSERT INTO t1(v) VALUES (1);
    ELSE INSERT INTO t2(v) VALUES (2);
  END CASE;
END;
$$;
