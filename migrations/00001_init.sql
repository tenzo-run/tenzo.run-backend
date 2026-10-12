-- +goose Up
create type lead_locale as enum ('ru', 'en', 'zh');

create table leads
(
    id         bigint generated always as identity primary key,
    public_id  uuid        not null,
    name       text        not null,
    contact    text        not null,
    locale     lead_locale not null,
    page       text        not null default '',
    ip         inet,
    created_at timestamptz not null default now(),
    constraint leads_public_id_key unique (public_id),
    constraint leads_name_check check (char_length(name) between 1 and 100),
    constraint leads_contact_check check (char_length(contact) between 3 and 200),
    constraint leads_page_check check (char_length(page) <= 300)
);

create index leads_created_at_idx on leads (created_at desc);

-- +goose Down
drop table leads;
drop type lead_locale;
