package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type quotaProvider struct {
	*mockProvider
	err error
}

func (p *quotaProvider) CheckQuota(context.Context) error { return p.err }

func TestDailyQuotaAggregator(t *testing.T) {
	ctx := context.Background()
	daily := &DailyQuotaError{ResetAt: time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)}
	primary := &mockProvider{name: "openlibrary"}
	for _, tc := range []struct {
		name string
		agg  *Aggregator
		want error
	}{
		{"nil", nil, nil},
		{"unsupported", NewAggregator(primary), nil},
		{"available", NewAggregator(&quotaProvider{primary, nil}), nil},
		{"primary held", NewAggregator(&quotaProvider{primary, daily}), daily},
		// Bulk work does not need an enricher, so its hold must not stop it.
		{"enricher held", NewAggregator(primary, &quotaProvider{&mockProvider{name: "hardcover"}, daily}), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.agg.CheckQuota(ctx); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	if !strings.Contains(daily.Error(), "2026-09-19T01:00:00Z") {
		t.Fatal(daily.Error())
	}
}

func TestDailyQuotaISBNStopsFallback(t *testing.T) {
	daily := &DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
	primary := &mockProvider{name: "hardcover", getByISBNErr: fmt.Errorf("lookup: %w", daily)}
	fallback := &mockProvider{name: "openlibrary"}
	book, _, err := NewAggregator(primary, fallback).GetBookByISBNWithOutcome(context.Background(), "9780441172719")
	if book != nil || !errors.Is(err, daily) || fallback.getByISBNCalls != 0 {
		t.Fatalf("book %v err %v fallback calls %d", book, err, fallback.getByISBNCalls)
	}
}

func TestDailyQuotaISBNEnricherHoldKeepsOtherProviders(t *testing.T) {
	daily := &DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
	const isbn = "9780441172719"
	found := &models.Book{ForeignID: "OL1W", Title: "Dune", Description: strings.Repeat("d", 60)}

	// The primary's answer survives a held enricher.
	primary := &mockProvider{name: "openlibrary", getByISBN: found}
	held := &mockProvider{name: "hardcover", getByISBNErr: fmt.Errorf("lookup: %w", daily)}
	book, _, err := NewAggregator(primary, held).GetBookByISBNWithOutcome(context.Background(), isbn)
	if err != nil || book == nil || book.ForeignID != "OL1W" {
		t.Fatalf("primary answer: book %v err %v", book, err)
	}

	// A primary miss still reaches the fallback after the held enricher.
	miss := &mockProvider{name: "openlibrary"}
	held = &mockProvider{name: "hardcover", getByISBNErr: fmt.Errorf("lookup: %w", daily)}
	fallback := &mockProvider{name: "dnb", getByISBN: &models.Book{ForeignID: "dnb:1", Title: "Dune", Description: strings.Repeat("d", 60)}}
	book, _, err = NewAggregator(miss, held, fallback).GetBookByISBNWithOutcome(context.Background(), isbn)
	if err != nil || book == nil || book.ForeignID != "dnb:1" || fallback.getByISBNCalls != 1 {
		t.Fatalf("fallback answer: book %v err %v calls %d", book, err, fallback.getByISBNCalls)
	}
}

func TestTokenRejectedIsItsOwnHold(t *testing.T) {
	rejected := &TokenRejectedError{Provider: "Hardcover", Cause: errors.New("token rejected (HTTP 401)")}
	daily := &DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
	for name, err := range map[string]error{
		"bare":    rejected,
		"wrapped": fmt.Errorf("lookup: %w", rejected),
		"joined":  errors.Join(rejected, errors.New("persist failed")),
	} {
		var asDaily *DailyQuotaError
		if !IsProviderHold(err) || errors.As(err, &asDaily) || !errors.Is(ProviderHold(err), rejected) {
			t.Errorf("%s: hold %v daily %v", name, IsProviderHold(err), errors.As(err, &asDaily))
		}
		if errors.Is(err, ErrRateLimited) || IsProviderUnavailable(err) {
			t.Errorf("%s: must not read as a throttle or an outage", name)
		}
	}
	if !IsProviderHold(daily) || ProviderHold(errors.New("other")) != nil || IsProviderHold(nil) {
		t.Fatal("ProviderHold misclassifies")
	}
	held := &TokenRejectedError{Provider: "Hardcover"}
	if msg := held.Error(); !strings.Contains(msg, "Hardcover rejected the API token") || strings.Contains(msg, "reset") {
		t.Fatal(msg)
	}
}

func TestTokenRejectedStopsPrimaryButNotEnricher(t *testing.T) {
	ctx := context.Background()
	rejected := &TokenRejectedError{Provider: "Hardcover"}
	primary := &mockProvider{name: "hardcover", getByISBNErr: fmt.Errorf("lookup: %w", rejected)}
	fallback := &mockProvider{name: "openlibrary"}
	if _, _, err := NewAggregator(primary, fallback).GetBookByISBNWithOutcome(ctx, "9780441172719"); !errors.Is(err, rejected) || fallback.getByISBNCalls != 0 {
		t.Fatalf("primary: err %v fallback calls %d", err, fallback.getByISBNCalls)
	}
	found := &models.Book{ForeignID: "OL1W", Title: "Dune", Description: strings.Repeat("d", 60)}
	ol := &mockProvider{name: "openlibrary", getByISBN: found}
	held := &mockProvider{name: "hardcover", getByISBNErr: fmt.Errorf("lookup: %w", rejected)}
	if book, _, err := NewAggregator(ol, held).GetBookByISBNWithOutcome(ctx, "9780441172719"); err != nil || book == nil {
		t.Fatalf("enricher hold stopped the lookup: %v %v", book, err)
	}
	if err := NewAggregator(&quotaProvider{&mockProvider{name: "hardcover"}, rejected}).CheckQuota(ctx); !errors.Is(err, rejected) {
		t.Fatalf("CheckQuota: %v", err)
	}
}
