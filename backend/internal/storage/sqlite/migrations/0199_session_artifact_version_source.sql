-- +goose Up
-- +goose StatementBegin
ALTER TABLE session_artifact_versions
    ADD COLUMN source TEXT NOT NULL DEFAULT 'agent' CHECK (source IN ('agent', 'user'));
ALTER TABLE session_artifact_versions
    ADD COLUMN harness TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE session_artifact_versions DROP COLUMN harness;
ALTER TABLE session_artifact_versions DROP COLUMN source;
-- +goose StatementEnd
