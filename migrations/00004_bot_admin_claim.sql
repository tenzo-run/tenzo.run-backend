-- +goose Up
alter table bot_admins
    add column claimed boolean not null default false;

-- +goose Down
alter table bot_admins
    drop column claimed;
