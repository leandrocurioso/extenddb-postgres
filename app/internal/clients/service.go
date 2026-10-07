package clients

import (
	"context"
	"fmt"
	"time"
)

// Service holds the client-domain rules; the Repository only knows storage.
type Service struct {
	repo *Repository
	cell string
}

func NewService(repo *Repository, cell string) *Service {
	return &Service{repo: repo, cell: cell}
}

// Register onboards a new client: PutItem for the profile (create-only), then
// BatchWriteItem for its addresses and contacts.
func (s *Service) Register(ctx context.Context, c *Client) (*Client, error) {
	if err := s.prepare(c); err != nil {
		return nil, err
	}
	if err := s.repo.PutClient(ctx, c); err != nil {
		return nil, err
	}
	if err := s.repo.WriteClientDetails(ctx, c); err != nil {
		return nil, fmt.Errorf("client %s created but details failed: %w", c.ID, err)
	}
	return c, nil
}

// Import bulk-loads clients: profiles go in BatchWriteItem calls, then each
// client's addresses and contacts.
func (s *Service) Import(ctx context.Context, cs []*Client) ([]*Client, error) {
	for _, c := range cs {
		if err := s.prepare(c); err != nil {
			return nil, fmt.Errorf("client %q: %w", c.Email, err)
		}
	}
	if err := s.repo.BatchPutClients(ctx, cs); err != nil {
		return nil, err
	}
	for _, c := range cs {
		if err := s.repo.WriteClientDetails(ctx, c); err != nil {
			return nil, err
		}
	}
	return cs, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Client, error) {
	return s.repo.GetClient(ctx, id)
}

func (s *Service) FindByEmail(ctx context.Context, email string) (*Client, error) {
	return s.repo.FindByEmail(ctx, email)
}

func (s *Service) prepare(c *Client) error {
	if err := c.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	if c.ID == "" {
		c.ID = NewID()
	}
	c.Status = StatusActive
	c.HomeCell = s.cell
	c.CreatedAt, c.UpdatedAt = now, now
	return nil
}
