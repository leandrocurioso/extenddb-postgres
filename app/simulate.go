package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/curioso/extenddb-postgres/app/internal/clients"
)

var (
	firstNames = []string{"Ana", "Bruno", "Carla", "Diego", "Elisa", "Fabio", "Gabriela", "Hugo", "Isabela", "Joao"}
	lastNames  = []string{"Silva", "Souza", "Costa", "Oliveira", "Pereira", "Lima", "Gomes", "Ribeiro", "Alves", "Rocha"}
	cities     = []struct{ City, State string }{{"Sao Paulo", "SP"}, {"Rio de Janeiro", "RJ"}, {"Curitiba", "PR"}, {"Recife", "PE"}, {"Porto Alegre", "RS"}}
	segments   = []string{"retail", "smb", "enterprise"}
)

// simulate continuously onboards synthetic clients from this cell, then reads
// them back, so every cell exercises PutItem, BatchWriteItem and Query against
// the shared Patroni cluster.
func simulate(ctx context.Context, svc *clients.Service, s settings, log *slog.Logger) {
	t := time.NewTicker(s.Simulate)
	defer t.Stop()
	for {
		start := time.Now()
		c, err := svc.Register(ctx, randomClient(s.Cell))
		if err != nil {
			log.Error("simulate: register failed", "err", err)
		} else if got, err := svc.Get(ctx, c.ID); err != nil {
			log.Error("simulate: read-back failed", "id", c.ID, "err", err)
		} else {
			log.Info("simulate: client registered",
				"id", got.ID, "email", got.Email,
				"addresses", len(got.Addresses), "contacts", len(got.Contacts),
				"elapsed", time.Since(start).String())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func randomClient(cell string) *clients.Client {
	first := firstNames[rand.IntN(len(firstNames))]
	last := lastNames[rand.IntN(len(lastNames))]
	loc := cities[rand.IntN(len(cities))]
	tag := rand.IntN(1_000_000)
	return &clients.Client{
		Name:     first + " " + last,
		Email:    fmt.Sprintf("%s.%s.%06d@%s.example.com", first, last, tag, cell),
		Document: fmt.Sprintf("%011d", rand.Int64N(100_000_000_000)),
		Segment:  segments[rand.IntN(len(segments))],
		Addresses: []clients.Address{
			{Kind: "billing", Street: fmt.Sprintf("Rua %s, %d", last, rand.IntN(2000)+1), City: loc.City, State: loc.State, Country: "BR", PostalCode: fmt.Sprintf("%05d-%03d", rand.IntN(100000), rand.IntN(1000))},
			{Kind: "shipping", Street: fmt.Sprintf("Avenida %s, %d", first, rand.IntN(2000)+1), City: loc.City, State: loc.State, Country: "BR", PostalCode: fmt.Sprintf("%05d-%03d", rand.IntN(100000), rand.IntN(1000))},
		},
		Contacts: []clients.Contact{
			{Kind: "mobile", Value: fmt.Sprintf("+55 11 9%04d-%04d", rand.IntN(10000), rand.IntN(10000))},
		},
	}
}
