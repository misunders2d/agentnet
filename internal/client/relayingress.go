package client

import (
	"context"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
)

type relayIngressKey struct{}

// Bind transport provenance to this exact carrier, not any child imported by
// a handler using the same context. Only the relay ingress boundary sets it.
func withRelayIngress(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, relayIngressKey{}, id)
}

// A received message is admitted on the one goroutine reading the push stream,
// so what it waits for the relay every later message and ping waits for too.
// readerBudgetKey carries when that wait ends: admitChainBudget for the whole
// message, across every request it makes, however many persons it names. It
// bounds requests only, never the store. A message past it waits held and is
// looked at again away from the stream (admissionFailed).
type readerBudgetKey struct{}

func withReaderBudget(ctx context.Context, until time.Time) context.Context {
	return context.WithValue(ctx, readerBudgetKey{}, until)
}

// withoutReaderBudget: a durable outcome hangs on this request's answer, so
// running out of the reader's time must not decide it.
func withoutReaderBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, readerBudgetKey{}, time.Time{})
}

func readerBudgetEnd(ctx context.Context) (time.Time, bool) {
	until, _ := ctx.Value(readerBudgetKey{}).(time.Time)
	return until, !until.IsZero()
}

// readerBounded is ctx for one request: it ends with the reader's budget.
func readerBounded(ctx context.Context) (context.Context, context.CancelFunc) {
	until, ok := readerBudgetEnd(ctx)
	if !ok {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, until)
}

func readerBudgetSpent(ctx context.Context) bool {
	until, ok := readerBudgetEnd(ctx)
	return ok && !time.Now().Before(until)
}

func relayIngress(ctx context.Context, id string) bool {
	got, _ := ctx.Value(relayIngressKey{}).(string)
	return got != "" && got == id
}
func retainedRelayOrdinary(in envelope.Inner) bool {
	if in.V != envelope.Version && in.V != envelope.Version2 {
		return false
	}
	if in.Kind != envelope.KindMessage && in.Kind != envelope.KindAnswer && in.Kind != envelope.KindResult {
		return false
	}
	if in.Kind == envelope.KindMessage && in.Status != "" {
		return false
	}
	if in.Kind == envelope.KindAnswer || in.Kind == envelope.KindResult {
		switch in.Status {
		case "", envelope.StatusDone, envelope.StatusFailed, envelope.StatusTimeout, envelope.StatusCancelled, envelope.StatusDeclined, envelope.StatusInterrupted:
		default:
			return false
		}
	}
	return in.Sub == "" && !in.Replica && in.Ref == nil && in.Target == nil && in.Followup == nil && in.TopicEvent == nil && !in.TopicDone && in.ReceiverRoute == nil
}
func (a *Agent) revokedIngressAllowed(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public) (bool, error) {
	revoked, err := revokedDeviceKnown(a.store.db, env.From, sender.Fingerprint())
	if err != nil {
		return false, err
	}
	return !revoked || relayIngress(ctx, env.ID) && retainedRelayOrdinary(in), nil
}
