-- +goose Up
CREATE TABLE gadget (
    id serial PRIMARY KEY,
    widget_id integer NOT NULL REFERENCES widget (id)
);

-- +goose Down
DROP TABLE gadget;
