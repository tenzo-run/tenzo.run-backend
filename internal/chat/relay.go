package chat

import "context"

type Posted struct {
	ChatID    int64
	MessageID int64
}

type Ticket struct {
	Posted []Posted
}

type Notice struct {
	Session    Session
	Returning  bool
	NewContact bool
	Text       string
	Reply      string
	Escalated  bool
}

type Relay interface {
	Incoming(ctx context.Context, n Notice) (Ticket, error)
	Answered(ctx context.Context, t Ticket, n Notice) error
}

type Rules interface {
	Texts(ctx context.Context) ([]string, error)
}
