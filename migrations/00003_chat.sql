-- +goose Up
create table chat_sessions
(
    id          bigint generated always as identity primary key,
    public_id   uuid        not null unique,
    channel     text        not null,
    telegram_id bigint,
    profile     text,
    locale      lead_locale not null,
    page        text,
    name        text,
    contact     text,
    project     text,
    budget      text,
    deadline    text,
    turns       jsonb       not null default '[]',
    waiting     boolean     not null default false,
    question    text,
    created_at  timestamptz not null,
    updated_at  timestamptz not null,
    constraint chat_sessions_channel_check check (channel in ('site', 'telegram')),
    constraint chat_sessions_telegram_check check ((channel = 'telegram') = (telegram_id is not null)),
    constraint chat_sessions_profile_check check (profile is null or char_length(profile) between 1 and 120),
    constraint chat_sessions_page_check check (page is null or char_length(page) between 1 and 200),
    constraint chat_sessions_name_check check (name is null or char_length(name) between 1 and 60),
    constraint chat_sessions_contact_check check (contact is null or char_length(contact) between 3 and 120),
    constraint chat_sessions_project_check check (project is null or char_length(project) between 1 and 600),
    constraint chat_sessions_budget_check check (budget is null or char_length(budget) between 1 and 120),
    constraint chat_sessions_deadline_check check (deadline is null or char_length(deadline) between 1 and 120),
    constraint chat_sessions_question_check check (question is null or char_length(question) between 1 and 500),
    constraint chat_sessions_turns_check check (jsonb_typeof(turns) = 'array')
);

create index chat_sessions_updated_idx on chat_sessions (updated_at desc, id desc);
create index chat_sessions_telegram_idx on chat_sessions (telegram_id, id desc) where telegram_id is not null;
create index chat_sessions_waiting_idx on chat_sessions (updated_at desc) where waiting;

create table bot_messages
(
    chat_id    bigint      not null,
    message_id bigint      not null,
    session_id bigint      not null references chat_sessions on delete cascade,
    created_at timestamptz not null,
    primary key (chat_id, message_id)
);

create index bot_messages_session_idx on bot_messages (session_id);

create table bot_updates
(
    update_id  bigint primary key,
    handled_at timestamptz not null
);

create table bot_admins
(
    user_id    bigint primary key,
    username   text        not null,
    name       text        not null,
    created_at timestamptz not null
);

create table assistant_rules
(
    id         bigint generated always as identity primary key,
    text       text        not null,
    created_by text        not null,
    created_at timestamptz not null,
    constraint assistant_rules_text_check check (char_length(text) between 1 and 500)
);

-- +goose Down
drop table assistant_rules;
drop table bot_admins;
drop table bot_updates;
drop table bot_messages;
drop table chat_sessions;
