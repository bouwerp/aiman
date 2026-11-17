-- aiman's read role for harness database reads. The postgres image's
-- entrypoint runs this once, as the superuser, when it creates the database,
-- with ON_ERROR_STOP set. The password is per run and comes from the
-- container environment, so this file holds no secret. When AIMAN_RO_PASSWORD
-- is unset the variable stays unset, psql leaves the reference unexpanded and
-- the statement fails, which fails the container's first start.
\getenv aiman_ro_password AIMAN_RO_PASSWORD
CREATE ROLE aiman_ro WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'aiman_ro_password';
GRANT pg_read_all_data TO aiman_ro;
