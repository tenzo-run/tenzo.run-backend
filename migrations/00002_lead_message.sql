-- +goose Up
alter table leads
    add column message text not null default '',
    add constraint leads_message_check check (char_length(message) <= 2000);

-- +goose Down
alter table leads
    drop constraint leads_message_check,
    drop column message;
