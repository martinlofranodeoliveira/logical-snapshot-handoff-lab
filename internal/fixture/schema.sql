CREATE TABLE public.items (
 id bigint PRIMARY KEY,
 label text NOT NULL CHECK (octet_length(label) <= 256),
 revision bigint NOT NULL CHECK (revision > 0)
);
ALTER TABLE public.items REPLICA IDENTITY DEFAULT;
CREATE PUBLICATION lab_pub FOR TABLE public.items
 WITH (publish = 'insert, update, delete, truncate');
