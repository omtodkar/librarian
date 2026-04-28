CREATE FUNCTION public.malformed_body() RETURNS void LANGUAGE plpgsql AS $$ SELECT 1 $$;
