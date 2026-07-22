-- +goose Up
ALTER TABLE widget ADD COLUMN color text NOT NULL DEFAULT 'unpainted';

-- +goose Down
ALTER TABLE widget DROP COLUMN color;
