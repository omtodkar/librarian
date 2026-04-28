CREATE FUNCTION public.merge_example() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  MERGE INTO target USING source ON target.id = source.id
  WHEN MATCHED THEN UPDATE SET name = source.name
  WHEN NOT MATCHED THEN INSERT (id, name) VALUES (source.id, source.name);
END;
$$;
