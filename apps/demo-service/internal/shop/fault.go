package shop

import (
	"context"
	"errors"
)

// ErrInjectedFault is the error a faulty repository returns. It reaches the
// client as an ordinary 500, and the span and log as a recorded error, so
// the "bad release" looks exactly like a real one from the outside.
var ErrInjectedFault = errors.New("injected fault (DEMO_FAULT_ERROR_RATE)")

// WithFaults returns repo with a share rate (0 to 1) of its calls failing.
// random returns a number in [0, 1), for example rand.Float64. Faults happen
// after routing and validation, so a faulty call is reported under its real
// route, and requests rejected with 400 or 404 are never affected.
func WithFaults(repo Repository, rate float64, random func() float64) Repository {
	if rate <= 0 {
		return repo
	}
	return &faultyRepository{next: repo, rate: rate, random: random}
}

type faultyRepository struct {
	next   Repository
	rate   float64
	random func() float64
}

func (f *faultyRepository) fail() bool {
	return f.random() < f.rate
}

func (f *faultyRepository) ListProducts(ctx context.Context, limit, offset int) ([]Product, error) {
	if f.fail() {
		return nil, ErrInjectedFault
	}
	return f.next.ListProducts(ctx, limit, offset)
}

func (f *faultyRepository) GetProduct(ctx context.Context, id int64) (Product, error) {
	if f.fail() {
		return Product{}, ErrInjectedFault
	}
	return f.next.GetProduct(ctx, id)
}

func (f *faultyRepository) OrdersByCustomer(ctx context.Context, email string, limit int) ([]Order, error) {
	if f.fail() {
		return nil, ErrInjectedFault
	}
	return f.next.OrdersByCustomer(ctx, email, limit)
}

func (f *faultyRepository) CreateOrder(ctx context.Context, email string, productID int64, quantity int32) (Order, error) {
	if f.fail() {
		return Order{}, ErrInjectedFault
	}
	return f.next.CreateOrder(ctx, email, productID, quantity)
}

func (f *faultyRepository) GetOrder(ctx context.Context, id int64) (Order, error) {
	if f.fail() {
		return Order{}, ErrInjectedFault
	}
	return f.next.GetOrder(ctx, id)
}
