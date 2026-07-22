-- +goose Up
CREATE TABLE widget (
    id serial PRIMARY KEY,
    name text NOT NULL
);

-- +goose Down
DROP TABLE widget;
